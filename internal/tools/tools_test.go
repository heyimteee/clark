package tools

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryRegisterAndList(t *testing.T) {
	r := NewRegistry()
	r.RegisterFunc("a", "tool a", nil, func(ctx context.Context, args map[string]any) (string, error) {
		return "A", nil
	})
	r.RegisterFunc("b", "tool b", nil, func(ctx context.Context, args map[string]any) (string, error) {
		return "B", nil
	})

	if got := len(r.List()); got != 2 {
		t.Errorf("List() = %d tools, want 2", got)
	}
	for _, name := range []string{"a", "b"} {
		if !r.Has(name) {
			t.Errorf("Has(%q) = false", name)
		}
	}
	if r.Has("c") {
		t.Errorf("Has(c) = true, want false")
	}
}

func TestRegistryRegisterOverwrites(t *testing.T) {
	r := NewRegistry()
	r.RegisterFunc("a", "first", nil, func(ctx context.Context, args map[string]any) (string, error) {
		return "first", nil
	})
	r.RegisterFunc("a", "second", nil, func(ctx context.Context, args map[string]any) (string, error) {
		return "second", nil
	})
	if got := len(r.List()); got != 1 {
		t.Errorf("List() = %d tools, want 1 after overwrite", got)
	}
}

func TestRegistryExecute(t *testing.T) {
	var got string
	r := NewRegistry()
	r.RegisterFunc("echo", "echo a string", nil, func(ctx context.Context, args map[string]any) (string, error) {
		got = StringArg(args, "value")
		return "done: " + got, nil
	})

	out, err := r.Execute(context.Background(), "echo", []byte(`{"value":"hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "hi" {
		t.Errorf("arg = %q, want hi", got)
	}
	if out != "done: hi" {
		t.Errorf("out = %q, want done: hi", out)
	}
}

func TestRegistryExecuteUnknown(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Execute(context.Background(), "nope", nil); err == nil {
		t.Fatal("Execute(unknown) succeeded, want error")
	}
}

func TestRegistryExecuteBadArgs(t *testing.T) {
	r := NewRegistry()
	r.RegisterFunc("a", "a", nil, func(ctx context.Context, args map[string]any) (string, error) {
		return "", nil
	})
	if _, err := r.Execute(context.Background(), "a", []byte("not-json")); err == nil {
		t.Fatal("Execute(bad json) succeeded, want error")
	}
}

func TestRegistryExecuteStringWrappedArgs(t *testing.T) {
	r := NewRegistry()
	var got string
	r.RegisterFunc("echo", "echo a string", nil, func(ctx context.Context, args map[string]any) (string, error) {
		got = StringArg(args, "value")
		return "done", nil
	})

	out, err := r.Execute(context.Background(), "echo", []byte(`"{\"value\":\"hi\"}"`))
	if err != nil {
		t.Fatalf("Execute(string-wrapped args): %v", err)
	}
	if got != "hi" {
		t.Errorf("arg = %q, want hi", got)
	}
	if out != "done" {
		t.Errorf("out = %q, want done", out)
	}
}

func TestStringAndBoolArg(t *testing.T) {
	args := map[string]any{"s": "x", "b": true, "n": 7}
	if StringArg(args, "s") != "x" {
		t.Errorf("StringArg(s) = %q", StringArg(args, "s"))
	}
	if !BoolArg(args, "b") {
		t.Errorf("BoolArg(b) = false")
	}
	if StringArg(args, "missing") != "" {
		t.Errorf("StringArg(missing) = %q", StringArg(args, "missing"))
	}
	if BoolArg(args, "s") || BoolArg(args, "n") {
		t.Errorf("BoolArg on non-bool returned true")
	}
	if strings.Contains(StringArg(args, "n"), "7") {
		t.Errorf("StringArg(n) leaked non-string value")
	}
}
