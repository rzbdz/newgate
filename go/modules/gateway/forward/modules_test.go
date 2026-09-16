package forward_test

import (
	"context"
	"os"
	"testing"

	"github.com/rzbdz/newgate/go/modules/builtin"
)

func TestMain(m *testing.M) {
	app, err := builtin.New(context.Background())
	if err != nil {
		panic(err)
	}
	code := m.Run()
	if err := app.Stop(context.Background()); err != nil && code == 0 {
		code = 1
	}
	os.Exit(code)
}
