package scoring

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }
func validEMA(m, e, a int) EMA {
	return EMA{Mood: ptr(m), Anhedonia: ptr(2), Energy: ptr(e), Anxiety: ptr(a), Suicidal: ptr(false)}
}
func TestCSDExcelFormulas(t *testing.T) {
	// Excel S=MOD(K-B,1)*1440, T=L/S, U=MOD(B+(D+L/2)/1440,1), V=F.
	cases := []struct {
		name    string
		c       CSD
		tib, se float64
		mid     string
	}{
		{"midnight", CSD{Bedtime: "23:00", RiseTime: "07:00", Latency: ptr(30.0), TotalSleep: ptr(420.0), AwakeMinutes: ptr(20.0)}, 480, .875, "03:00"},
		{"daytime", CSD{Bedtime: "09:00", RiseTime: "17:00", Latency: ptr(15.0), TotalSleep: ptr(450.0)}, 480, .9375, "13:00"},
		{"midpoint wraps", CSD{Bedtime: "23:50", RiseTime: "08:00", Latency: ptr(20.0), TotalSleep: ptr(360.0)}, 490, 360.0 / 490, "03:10"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, err := CalculateCSD(tt.c)
			if err != nil {
				t.Fatal(err)
			}
			if s.TIB == nil || math.Abs(*s.TIB-tt.tib) > 1e-8 || s.SE == nil || math.Abs(*s.SE-tt.se) > 1e-8 || s.Midpoint == nil || *s.Midpoint != tt.mid {
				t.Fatalf("unexpected result: %+v", s)
			}
			if tt.c.AwakeMinutes != nil && (s.WASO == nil || *s.WASO != *tt.c.AwakeMinutes) {
				t.Fatal("WASO mismatch")
			}
		})
	}
}
func TestCSDMissingAndInvalid(t *testing.T) {
	for _, c := range []CSD{{}, {Bedtime: "23:00"}, {RiseTime: "07:00"}, {Bedtime: "23:00", RiseTime: "23:00", TotalSleep: ptr(0.0)}} {
		s, err := CalculateCSD(c)
		if err != nil || s.SE != nil || s.Midpoint != nil {
			t.Fatalf("missing/zero cells should remain null: %+v %v", s, err)
		}
	}
	for _, c := range []CSD{{Bedtime: "25:00"}, {SleepAttempt: "bad"}, {Latency: ptr(-1.0)}, {TotalSleep: ptr(math.Inf(1))}, {Awakenings: ptr(-1)}, {Quality: ptr(0)}, {Rested: ptr(6)}, {Comment: string(make([]rune, 2001))}} {
		if _, err := CalculateCSD(c); err == nil {
			t.Fatalf("invalid input accepted: %+v", c)
		}
	}
}
func TestEMAValidation(t *testing.T) {
	good := validEMA(0, 10, 0)
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, e := range []EMA{{}, {Mood: ptr(-1)}, {Mood: ptr(11)}, {Mood: ptr(1), Anhedonia: ptr(1), Energy: ptr(1), Anxiety: ptr(1)}, {Mood: ptr(1), Anhedonia: ptr(1), Energy: ptr(1), Anxiety: ptr(1), Suicidal: ptr(false), Comment: string(make([]rune, 2001))}} {
		if e.Validate() == nil {
			t.Fatal("invalid EMA accepted")
		}
	}
	var e EMA
	if err := json.Unmarshal([]byte(`{"mood":1.5}`), &e); err == nil {
		t.Fatal("fractional score accepted")
	}
}
func TestEMASpreadsheetSummary(t *testing.T) {
	end := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	a, b, c := validEMA(2, 9, 1), validEMA(4, 7, 8), validEMA(5, 5, 3)
	b.Suicidal = ptr(true)
	days := []DailyEMA{{"2026-10-23", a}, {"2026-10-24", b}, {"2026-10-25", c}, {"2026-10-20", c}, {"2026-10-24", b}}
	s := SummarizeEMA(days, end)
	if !s.Complete || s.Count != 3 || s.SuicidalDays != 1 || *s.MoodDelta != 3 || *s.MaxAnxiety != 8 || *s.MinEnergy != 5 {
		t.Fatalf("summary mismatch %+v", s)
	}
	if math.Abs(*s.Mood-11.0/3) > 1e-9 || *s.Anhedonia != 2 || *s.Energy != 7 || *s.Anxiety != 4 {
		t.Fatal("means/scale direction changed")
	}
	partial := SummarizeEMA(days[1:3], end)
	if partial.Complete || partial.MoodDelta != nil {
		t.Fatal("partial window must not substitute adjacent days")
	}
	empty := SummarizeEMA([]DailyEMA{{"2026-10-25", EMA{}}}, end)
	if empty.Mood != nil || empty.Count != 0 {
		t.Fatal("missing responses treated as zero")
	}
}
