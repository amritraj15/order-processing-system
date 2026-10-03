package validator

import (
	"errors"
	"reflect"
	"strings"

	v "github.com/go-playground/validator/v10"
)

type ValidationError struct{ Details map[string]string }

func (*ValidationError) Error() string { return "validation failed" }

var validate = func() *v.Validate {
	validator := v.New(v.WithRequiredStructEnabled())
	validator.RegisterTagNameFunc(func(f reflect.StructField) string { name, _, _ := strings.Cut(f.Tag.Get("json"), ","); return name })
	return validator
}()

func Struct(value any) error {
	err := validate.Struct(value)
	if err == nil {
		return nil
	}
	var fields v.ValidationErrors
	if !errors.As(err, &fields) {
		return err
	}
	details := make(map[string]string, len(fields))
	for _, field := range fields {
		_, path, _ := strings.Cut(field.Namespace(), ".")
		details[path] = "failed " + field.Tag() + " validation"
	}
	return &ValidationError{Details: details}
}
