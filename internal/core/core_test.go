package core

import (
	"encoding/json"
	"testing"
)

func TestRatReducesAndNormalizesSign(t *testing.T) {
	cases := []struct {
		num, den int64
		want     Rational
	}{
		{2, 4, Rational{1, 2}},
		{-2, 4, Rational{-1, 2}},
		{2, -4, Rational{-1, 2}},
		{-2, -4, Rational{1, 2}},
		{0, -7, Rational{0, 1}},
		{5, 5, Rational{1, 1}},
	}
	for _, c := range cases {
		if got := Rat(c.num, c.den); got != c.want {
			t.Errorf("Rat(%d,%d)=%+v want %+v", c.num, c.den, got, c.want)
		}
	}
}

func TestRationalArithmetic(t *testing.T) {
	a := Rat(1, 3)
	b := Rat(1, 6)
	if got := a.Add(b).String(); got != "1/2" {
		t.Fatalf("add=%s", got)
	}
	if got := a.Sub(b).String(); got != "1/6" {
		t.Fatalf("sub=%s", got)
	}
	if got := a.Mul(b).String(); got != "1/18" {
		t.Fatalf("mul=%s", got)
	}
	if got := a.Div(b).String(); got != "2/1" {
		t.Fatalf("div=%s", got)
	}
	if a.Cmp(b) != 1 || b.Cmp(a) != -1 || a.Cmp(a) != 0 {
		t.Fatal("comparison wrong")
	}
	if got := Rat(-3, 2).Abs().String(); got != "3/2" {
		t.Fatalf("abs=%s", got)
	}
}

func TestRationalJSONRoundTrip(t *testing.T) {
	type wrapper struct {
		V Rational `json:"v"`
	}
	in := wrapper{V: Rat(-7, 12)}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"v":"-7/12"}` {
		t.Fatalf("json=%s", data)
	}
	var out wrapper
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.V != in.V {
		t.Fatalf("round trip=%+v", out.V)
	}
}

func TestRationalUnmarshalRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "1", "/2", "1/", "a/2", "1/b", "1/0"} {
		var r Rational
		err := r.UnmarshalText([]byte(s))
		if s == "1/0" {
			if err != nil {
				t.Fatalf("%q: unexpected error %v", s, err)
			}
			if !r.Zero() {
				t.Fatalf("%q decoded to %+v", s, r)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q: want error", s)
		}
	}
}

func TestSpanOps(t *testing.T) {
	s, err := NewSpan(10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 10 || s.Empty() || !s.Contains(10) || s.Contains(20) {
		t.Fatalf("span=%+v", s)
	}
	if !s.Within(20) || s.Within(19) {
		t.Fatal("within wrong")
	}
	u := s.Union(Span{Start: 5, End: 15})
	if u != (Span{Start: 5, End: 20}) {
		t.Fatalf("union=%+v", u)
	}
	if _, err := NewSpan(5, 4); err == nil {
		t.Fatal("want invalid span error")
	}
}

func TestHashPartsIsUnambiguous(t *testing.T) {
	if HashParts([]byte("ab"), []byte("c")) == HashParts([]byte("a"), []byte("bc")) {
		t.Fatal("length prefix must distinguish concatenations")
	}
	if HashParts([]byte("x")) != HashParts([]byte("x")) {
		t.Fatal("hash is not stable")
	}
}

func TestCanonicalJSONStable(t *testing.T) {
	v := map[string]int{"b": 2, "a": 1}
	a, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CanonicalJSON(v)
	if string(a) != string(b) {
		t.Fatal("canonical JSON is not stable")
	}
	if string(a) != `{"a":1,"b":2}` {
		t.Fatalf("canonical JSON=%s", a)
	}
}
