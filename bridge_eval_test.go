package mygo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// evalIn is the engine under Page.Eval and EmbeddedWebView.Eval: these
// tests run it against a fake page, without a window.

func TestEvalInExpression(t *testing.T) {
	var body string
	raw, err := evalIn(context.Background(), nil, "1 + 1", func(b string, cb func(string, error)) error {
		body = b
		cb(`{"ok":true,"v":2}`, nil)
		return nil
	})
	if err != nil {
		t.Fatalf("evalIn: %v", err)
	}
	if string(raw) != "2" {
		t.Fatalf("result = %s, want 2", raw)
	}
	if !strings.Contains(body, "1 + 1") || !strings.Contains(body, "JSON.stringify") {
		t.Fatalf("call got %q, want the expression wrapper", body)
	}
}

func TestEvalInBodyFallback(t *testing.T) {
	calls := 0
	raw, err := evalIn(context.Background(), nil, "const a = 1;\nreturn a", func(body string, cb func(string, error)) error {
		calls++
		if calls == 1 {
			if !strings.Contains(body, "const a = 1;") {
				t.Errorf("first call got %q, want the code as an expression", body)
			}
			cb("", errors.New("SyntaxError: Unexpected token 'const'"))
			return nil
		}
		if !strings.Contains(body, "async()=>") || !strings.Contains(body, "return a") {
			t.Errorf("second call got %q, want the async function body", body)
		}
		cb(`{"ok":true,"v":1}`, nil)
		return nil
	})
	if err != nil {
		t.Fatalf("evalIn: %v", err)
	}
	if string(raw) != "1" || calls != 2 {
		t.Fatalf("result = %s after %d calls, want 1 after 2", raw, calls)
	}
}

func TestEvalInJSError(t *testing.T) {
	_, err := evalIn(context.Background(), nil, "throw new Error('boom')", func(_ string, cb func(string, error)) error {
		cb(`{"ok":false,"e":"boom"}`, nil)
		return nil
	})
	var e *EvalError
	if !errors.As(err, &e) || e.Message != "boom" {
		t.Fatalf("err = %v, want EvalError boom", err)
	}
}

func TestEvalInClosedPage(t *testing.T) {
	_, err := evalIn(context.Background(), nil, "1", func(_ string, _ func(string, error)) error {
		return errDestroyed
	})
	if !errors.Is(err, errDestroyed) {
		t.Fatalf("err = %v, want errDestroyed", err)
	}
}

func TestEvalInCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := evalIn(ctx, nil, "1", func(_ string, _ func(string, error)) error {
		return nil // the page never answers
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
