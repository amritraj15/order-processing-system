package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"order_management/configs"
	database "order_management/db/gorm"
	"order_management/domain/pricing"
	"order_management/domain/shared"
	"order_management/domain/user"
	"order_management/internal/bootstrap"
	"order_management/internal/logging"
	"order_management/service/product"
)

func main() {
	logger := slog.New(logging.Handler{Handler: slog.NewJSONHandler(os.Stdout, nil)})
	slog.SetDefault(logger)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], logger); err != nil {
		logger.ErrorContext(ctx, "command failed", "error_kind", "command")
		fmt.Fprintln(os.Stderr, safeCommandError(err))
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: orders server | migrate [up|down] | admin --email EMAIL --name NAME | seed")
	}
	cfg, err := configs.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "server":
		if len(args) != 1 {
			return errors.New("server accepts no arguments")
		}
		return bootstrap.RunServer(ctx, cfg, logger)
	case "migrate":
		down := false
		if len(args) > 2 {
			return errors.New("usage: migrate [up|down]")
		}
		if len(args) == 2 {
			switch args[1] {
			case "down":
				down = true
			case "up":
			default:
				return errors.New("usage: migrate [up|down]")
			}
		}
		return database.Migrate(ctx, cfg.DatabaseURL, down)
	case "catalog", "rates", "admin", "seed":
		db, err := database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		sql, err := db.DB()
		if err != nil {
			return err
		}
		defer sql.Close()
		if args[0] == "catalog" || args[0] == "rates" {
			return pricingCommand(ctx, args, &database.PricingRepository{DB: db}, logger)
		}
		if args[0] == "admin" {
			flags := flag.NewFlagSet("admin", flag.ContinueOnError)
			email := flags.String("email", "", "admin email")
			name := flags.String("name", "Admin", "admin name")
			if err := flags.Parse(args[1:]); err != nil {
				return err
			}
			if len(flags.Args()) > 0 {
				return errors.New("unexpected admin arguments")
			}
			*email = strings.ToLower(strings.TrimSpace(*email))
			*name = strings.TrimSpace(*name)
			address, err := mail.ParseAddress(*email)
			if err != nil || address.Address != *email || len(*email) > 255 || len(*name) < 2 || len(*name) > 64 {
				return errors.New("valid email and name (2–64 bytes) required")
			}
			hash, err := user.HashPassword(os.Getenv("ADMIN_PASSWORD"))
			if err != nil {
				return fmt.Errorf("ADMIN_PASSWORD: %w", err)
			}
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			return (&database.UserRepository{DB: db}).Insert(ctx, &user.User{ID: id, Email: *email, Name: *name, PasswordHash: hash, Role: user.Admin, Active: true, CreatedAt: now, UpdatedAt: now})
		}
		if len(args) != 1 {
			return errors.New("seed accepts no arguments")
		}
		if _, err := bootstrap.Catalog(ctx, &database.PricingRepository{DB: db}, cfg); err != nil {
			return err
		}
		service := &product.Service{Repo: &database.ProductRepository{DB: db}}
		for _, cmd := range []product.CreateCommand{{SKU: "BOOK-001", Name: "Notebook", PriceMinor: 1299}, {SKU: "PEN-001", Name: "Pen", PriceMinor: 299}} {
			if _, err := service.HandleCreate(ctx, cmd); err != nil && !errors.Is(err, shared.ErrConflict) {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func safeCommandError(err error) string {
	if errors.Is(err, shared.ErrConflict) || errors.Is(err, shared.ErrInvalid) {
		return err.Error()
	}
	return "command failed; verify configuration, database access and schema"
}
func pricingCommand(ctx context.Context, args []string, repo pricing.Repository, logger *slog.Logger) error {
	if len(args) < 2 {
		return errors.New("catalog init or rates add required")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	if args[0] == "catalog" {
		if args[1] != "init" {
			return shared.ErrInvalid
		}
		currency := flags.String("currency", "", "confirmed base currency")
		confirm := flags.Bool("confirm-existing", false, "confirm existing product denomination")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if len(flags.Args()) != 0 {
			return shared.ErrInvalid
		}
		settings, err := repo.Initialize(ctx, pricing.InitializeInput{Currency: *currency, ConfirmExisting: *confirm})
		if err != nil {
			return err
		}
		logger.InfoContext(ctx, "catalog initialized", "actor_type", "operator", "action", "catalog.init", "currency", settings.BaseCurrency, "outcome", "committed")
		return nil
	}
	if args[1] != "add" {
		return shared.ErrInvalid
	}
	target := flags.String("target", "", "target currency")
	rate := flags.String("rate", "", "decimal target/base rate")
	from := flags.String("valid-from", "", "UTC RFC3339")
	until := flags.String("valid-until", "", "UTC RFC3339")
	source := flags.String("source", "", "non-secret rate label")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return shared.ErrInvalid
	}
	start, err := time.Parse(time.RFC3339, *from)
	if err != nil {
		return fmt.Errorf("%w: invalid valid-from", shared.ErrInvalid)
	}
	end, err := time.Parse(time.RFC3339, *until)
	if err != nil {
		return fmt.Errorf("%w: invalid valid-until", shared.ErrInvalid)
	}
	value := &pricing.Rate{TargetCurrency: *target, Rate: *rate, ValidFrom: start.UTC(), ValidUntil: end.UTC(), Source: *source}
	if err := repo.ImportRate(ctx, value); err != nil {
		return err
	}
	logger.InfoContext(ctx, "rate imported", "actor_type", "operator", "action", "rate.import", "resource_id", value.ID, "outcome", "committed")
	return nil
}
