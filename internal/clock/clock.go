package clock

import (
	"order_management/ports"
	"time"
)

type Real struct{}

func (Real) Now() time.Time { return time.Now() }

var _ ports.Clock = Real{}
