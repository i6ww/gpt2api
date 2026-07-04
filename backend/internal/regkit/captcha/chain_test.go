package captcha

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeSolver 单元测试用的桩。
type fakeSolver struct {
	name      string
	arkErr    error
	arkToken  string
	turnErr   error
	turnToken string
	delay     time.Duration
	calls     int
}

func (f *fakeSolver) Name() string { return f.name }

func (f *fakeSolver) SolveArkose(ctx context.Context, _ *ArkoseTask) (string, error) {
	f.calls++
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.arkToken, f.arkErr
}

func (f *fakeSolver) SolveTurnstile(ctx context.Context, _ *TurnstileTask) (string, error) {
	f.calls++
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.turnToken, f.turnErr
}

func TestChainSolver_FirstSuccess(t *testing.T) {
	a := &fakeSolver{name: "a", arkToken: "tok-a"}
	b := &fakeSolver{name: "b", arkToken: "tok-b"}
	c := NewChain(a, b)
	tok, err := c.SolveArkose(context.Background(), &ArkoseTask{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tok != "tok-a" {
		t.Fatalf("want first solver token, got %q", tok)
	}
	if b.calls != 0 {
		t.Fatalf("second solver should not be called on first success; calls=%d", b.calls)
	}
}

func TestChainSolver_FallbackOnError(t *testing.T) {
	a := &fakeSolver{name: "a", arkErr: errors.New("a: timeout")}
	b := &fakeSolver{name: "b", arkToken: "tok-b"}
	c := NewChain(a, b)
	tok, err := c.SolveArkose(context.Background(), &ArkoseTask{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tok != "tok-b" {
		t.Fatalf("expected second solver to win, got %q", tok)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("expected both solvers tried; a=%d b=%d", a.calls, b.calls)
	}
}

func TestChainSolver_AllFail(t *testing.T) {
	a := &fakeSolver{name: "a", arkErr: errors.New("a: timeout")}
	b := &fakeSolver{name: "b", arkErr: errors.New("b: UNSOLVABLE")}
	c := NewChain(a, b)
	_, err := c.SolveArkose(context.Background(), &ArkoseTask{})
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	if !strings.Contains(err.Error(), "a: timeout") || !strings.Contains(err.Error(), "b: UNSOLVABLE") {
		t.Fatalf("expected aggregated errors, got %v", err)
	}
}

func TestChainSolver_SkipsNotConfigured(t *testing.T) {
	a := &fakeSolver{name: "a", arkErr: ErrNotConfigured}
	b := &fakeSolver{name: "b", arkToken: "tok-b"}
	c := NewChain(a, b)
	tok, err := c.SolveArkose(context.Background(), &ArkoseTask{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tok != "tok-b" {
		t.Fatalf("expected b to be reached, got %q", tok)
	}
}

func TestChainSolver_AllNotConfigured(t *testing.T) {
	a := &fakeSolver{name: "a", arkErr: ErrNotConfigured}
	b := &fakeSolver{name: "b", arkErr: ErrNotConfigured}
	c := NewChain(a, b)
	_, err := c.SolveArkose(context.Background(), &ArkoseTask{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestChainSolver_PerAttemptTimeout(t *testing.T) {
	// solver a 故意 sleep 200ms；PerAttempt=50ms 应当让 a 被 ctx 中断、立刻切到 b。
	a := &fakeSolver{name: "a", delay: 200 * time.Millisecond, arkToken: "tok-a"}
	b := &fakeSolver{name: "b", arkToken: "tok-b"}
	c := NewChain(a, b)
	c.PerAttempt = 50 * time.Millisecond
	start := time.Now()
	tok, err := c.SolveArkose(context.Background(), &ArkoseTask{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tok != "tok-b" {
		t.Fatalf("expected b to win after a timed out, got %q", tok)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("per-attempt timeout did not kick in; elapsed=%s", elapsed)
	}
}

func TestChainSolver_ContextCancel(t *testing.T) {
	a := &fakeSolver{name: "a", arkErr: errors.New("a: down")}
	b := &fakeSolver{name: "b", delay: 200 * time.Millisecond, arkToken: "tok-b"}
	c := NewChain(a, b)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.SolveArkose(ctx, &ArkoseTask{})
	if err == nil {
		t.Fatal("expected ctx canceled error")
	}
}

func TestChainSolver_Name(t *testing.T) {
	c := NewChain(&fakeSolver{name: "a"}, &fakeSolver{name: "b"}, &fakeSolver{name: "c"})
	if got, want := c.Name(), "chain(a+b+c)"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	c1 := NewChain(&fakeSolver{name: "only"})
	if got, want := c1.Name(), "only"; got != want {
		t.Fatalf("single-solver chain Name() = %q, want %q", got, want)
	}
}
