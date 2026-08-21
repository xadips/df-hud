//go:build !windows

package hotkeys

import "context"

func (*Hotkeys) run(ctx context.Context) {
	<-ctx.Done()
}
