package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/doctor"
)

func TestUIDiagnosticsNeverFetchesPrices(t *testing.T) {
	doctorMachine(t)
	old := doctorOptions
	t.Cleanup(func() { doctorOptions = old })
	var fetch []bool
	doctorOptions = func(f bool) doctor.Options {
		fetch = append(fetch, f)
		opts := old(f)
		opts.RefreshPrices = func(context.Context) error { t.Error("price list fetched"); return nil }
		return opts
	}
	report := uiDiagnostics(context.Background())
	if len(report.Checks) == 0 || len(fetch) != 1 || fetch[0] {
		t.Fatalf("checks %d, fetch flags %v", len(report.Checks), fetch)
	}
}

func TestUICommandFlags(t *testing.T) {
	out, _, err := run(t, "ui", "--help")
	if err != nil || !strings.Contains(out, "--no-open") || !strings.Contains(out, "--port") {
		t.Fatal(out, err)
	}
	for _, port := range []string{"-1", "65536"} {
		_, _, err = run(t, "ui", "--port", port, "--no-open")
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Fatal(err)
		}
	}
}
