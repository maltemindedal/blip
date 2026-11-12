package server

import (
	"bytes"
	"encoding/json"
	"testing"
	"testing/quick"
)

// TestNormalizeMessageMatchesEncodingJSON pins the hand-rolled encoder to the
// standard library's output. If they ever diverge, the fast path is wrong.
func TestNormalizeMessageMatchesEncodingJSON(t *testing.T) {
	t.Parallel()

	contents := []string{
		"",
		"hello",
		`quotes " and \ backslash`,
		"control\x00\x01\x1f chars",
		"newline\nreturn\rtab\t",
		"html <script>&</script>",
		"unicode: äöü 日本語 🎉",
		"line separators: \u2028\u2029",
		"invalid utf8: " + string([]byte{0xff, 0xfe}),
	}

	for _, content := range contents {
		raw, err := json.Marshal(Message{Content: content})
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}

		got, err := normalizeMessage(raw)
		if err != nil {
			t.Fatalf("normalizeMessage(%q): %v", content, err)
		}

		// The baseline is the original unmarshal-then-marshal round trip.
		// Comparing against raw would be wrong for inputs containing invalid
		// UTF-8, which json.Marshal replaces on the way in.
		var decoded Message
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal fixture: %v", err)
		}
		want, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("marshal baseline: %v", err)
		}

		if !bytes.Equal(got, want) {
			t.Errorf("normalizeMessage(%q) = %s, want %s", content, got, want)
		}
	}
}

// TestAppendJSONStringExhaustiveRunes walks every rune the encoder can be
// handed, which pins the short-form escapes (\b, \f, \n, \r, \t) and the
// \u00XX fallback that quick.Check only reaches by chance.
func TestAppendJSONStringExhaustiveRunes(t *testing.T) {
	t.Parallel()

	for r := rune(0); r <= 0x2FFF; r++ {
		s := "a" + string(r) + "z"
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal %U: %v", r, err)
		}

		if got := appendJSONString(nil, s); !bytes.Equal(got, want) {
			t.Fatalf("appendJSONString(%U) = %s, want %s", r, got, want)
		}
	}
}

// TestAppendJSONStringQuick fuzzes the encoder against encoding/json.
func TestAppendJSONStringQuick(t *testing.T) {
	t.Parallel()

	matches := func(s string) bool {
		want, err := json.Marshal(s)
		if err != nil {
			return false
		}
		return bytes.Equal(appendJSONString(nil, s), want)
	}

	if err := quick.Check(matches, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// TestNormalizeMessageQuick fuzzes the whole normalizer, which exercises both
// the canonical fast path and the re-encoding slow path, against the
// unmarshal-then-marshal baseline it replaced.
func TestNormalizeMessageQuick(t *testing.T) {
	t.Parallel()

	matches := func(content string) bool {
		raw, err := json.Marshal(Message{Content: content})
		if err != nil {
			return false
		}

		got, err := normalizeMessage(raw)
		if err != nil {
			return false
		}

		var decoded Message
		if err = json.Unmarshal(raw, &decoded); err != nil {
			return false
		}
		want, err := json.Marshal(decoded)
		if err != nil {
			return false
		}

		return bytes.Equal(got, want)
	}

	if err := quick.Check(matches, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// TestNormalizeMessageDropsUnknownFields verifies the normalization contract:
// only the documented protocol field survives.
func TestNormalizeMessageDropsUnknownFields(t *testing.T) {
	t.Parallel()

	got, err := normalizeMessage([]byte(`{"content":"hi","admin":true,"extra":[1,2,3]}`))
	if err != nil {
		t.Fatalf("normalizeMessage: %v", err)
	}

	if want := `{"content":"hi"}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNormalizeMessageRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	if _, err := normalizeMessage([]byte(`{"content":`)); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

func BenchmarkNormalizeMessage(b *testing.B) {
	raw := []byte(`{"content":"the quick brown fox jumps over the lazy dog"}`)
	b.ReportAllocs()

	for b.Loop() {
		if _, err := normalizeMessage(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// TestNormalizeMessageAllocatesOnlyThePayload pins a canonical message at one
// allocation, the copy normalizeMessage returns. It cannot run in parallel:
// AllocsPerRun counts every allocation in the process and panics inside a
// parallel test.
//
// It covers the canonical fast path only. A message that needs the JSON round
// trip allocates more, and nothing bounds by how much.
func TestNormalizeMessageAllocatesOnlyThePayload(t *testing.T) {
	raw := []byte(`{"content":"the quick brown fox jumps over the lazy dog"}`)

	allocs := testing.AllocsPerRun(1000, func() {
		if _, err := normalizeMessage(raw); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("normalizeMessage allocates %v times for a canonical message, want 1", allocs)
	}
}

// BenchmarkNormalizeMessageStdlib is the previous implementation, kept as a
// baseline for the encoder above.
func BenchmarkNormalizeMessageStdlib(b *testing.B) {
	raw := []byte(`{"content":"the quick brown fox jumps over the lazy dog"}`)
	b.ReportAllocs()

	for b.Loop() {
		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(msg); err != nil {
			b.Fatal(err)
		}
	}
}
