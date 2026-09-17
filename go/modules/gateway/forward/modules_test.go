package forward_test

import (
	"context"
	"os"
	"testing"

	"github.com/rzbdz/newgate/go/app"
)

func TestMain(m *testing.M) {
	built, err := app.New(context.Background())
	if err != nil {
		panic(err)
	}
	code := m.Run()
	if err := built.Stop(context.Background()); err != nil && code == 0 {
		code = 1
	}
	os.Exit(code)
}
