//go:build !windows

package qqnt

import (
	"context"
	"fmt"
)

func (a *Adapter) launch(_ context.Context, _, _ string) error {
	return fmt.Errorf("automatic QQNT launch is supported only on Windows")
}
