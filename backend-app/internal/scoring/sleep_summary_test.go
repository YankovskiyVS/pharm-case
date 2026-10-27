package scoring

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestCSDSourceHasNoInventedUpperCap(t *testing.T) {
	c := CSD{Alcohol: ptr(1500.5), Caffeine: ptr(1441.0), Awakenings: ptr(1441), TotalSleep: ptr(1600.0)}
	if _, err := CalculateCSD(c); err != nil {
		t.Fatalf("source accepts nonnegative numbers without a 1440 maximum: %v", err)
	}
	for _, c := range []CSD{{Alcohol: ptr(-.5)}, {Caffeine: ptr(math.NaN())}, {Awakenings: ptr(-1)}} {
		if _, err := CalculateCSD(c); err == nil {
			t.Fatal("source-invalid input accepted")
		}
	}
}
func TestCSDSummaryMatchesSuppliedWorkbook(t *testing.T) {
	end := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	days := []DailyCSD{
		{"2026-10-12", CSD{Bedtime: "23:00", RiseTime: "07:00", Latency: ptr(20.0), Awakenings: ptr(2), AwakeMinutes: ptr(10.0), TotalSleep: ptr(420.0), Quality: ptr(4), Rested: ptr(3), NapMinutes: ptr(0.0), Alcohol: ptr(0.0), Caffeine: ptr(2.5)}},
		{"2026-10-25", CSD{Bedtime: "00:00", RiseTime: "08:00", Latency: ptr(40.0), Awakenings: ptr(4), AwakeMinutes: ptr(30.0), TotalSleep: ptr(360.0), Quality: ptr(2), Rested: ptr(1), NapMinutes: ptr(30.0), Alcohol: ptr(1.5), Caffeine: ptr(1.0)}},
		{"2026-10-24", CSD{Bedtime: "23:00", RiseTime: "23:00", TotalSleep: ptr(300.0)}},
		{"2026-10-11", CSD{Latency: ptr(999.0)}},
		{"2026-10-26", CSD{Latency: ptr(999.0)}},
		{"2026-10-25", CSD{Latency: ptr(999.0)}},
	}
	s := SummarizeCSD(days, end)
	expected := map[string]struct {
		value *float64
		want  float64
	}{"latency": {s.MeanLatency, 30}, "awakenings": {s.MeanAwakenings, 3}, "waso": {s.MeanWASO, 20}, "total_sleep": {s.MeanTotalSleep, 360}, "se": {s.MeanSE, .8125}, "quality": {s.MeanQuality, 3}, "rested": {s.MeanRested, 2}, "nap_minutes": {s.MeanNapMinutes, 15}}
	for name, tt := range expected {
		t.Run(name, func(t *testing.T) {
			if tt.value == nil || math.Abs(*tt.value-tt.want) > 1e-9 {
				t.Fatalf("expected %v got %v", tt.want, tt.value)
			}
		})
	}
	if s.From != "2026-10-12" || s.To != "2026-10-25" || s.Count != 3 || s.Complete {
		t.Fatalf("incorrect calendar window: %+v", s)
	}
	if s.TotalAlcohol != 1.5 || s.TotalCaffeine != 3.5 || s.Samples["se"] != 2 || s.Samples["total_sleep"] != 3 || s.Samples["quality"] != 2 {
		t.Fatalf("incorrect missing-cell counts/sums: %+v", s)
	}
}
func TestCSDSummaryMissingCellsAndCompleteWindow(t *testing.T) {
	end := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	empty := SummarizeCSD(nil, end)
	if empty.MeanLatency != nil || empty.MeanSE != nil || empty.TotalAlcohol != 0 || empty.TotalCaffeine != 0 || empty.Count != 0 || empty.Complete {
		t.Fatalf("empty Excel ranges differ: %+v", empty)
	}
	cases := []DailyCSD{{"bad-date", CSD{}}, {"2026-10-20", CSD{Quality: ptr(0)}}}
	if invalid := SummarizeCSD(cases, end); invalid.Count != 0 {
		t.Fatal("invalid dates/answers included")
	}
	days := []DailyCSD{}
	for i := 0; i < 14; i++ {
		days = append(days, DailyCSD{end.AddDate(0, 0, -i).Format("2006-01-02"), CSD{TotalSleep: ptr(400.0)}})
	}
	full := SummarizeCSD(days, end)
	if !full.Complete || full.Count != 14 || full.Samples["total_sleep"] != 14 || *full.MeanTotalSleep != 400 || full.MeanSE != nil {
		t.Fatalf("full window/missing denominators differ: %+v", full)
	}
}

func TestCSDDerivedValuesRemainJSONRepresentable(t *testing.T) {
	end := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	extreme := CSD{Bedtime: "23:00", RiseTime: "07:00", Latency: ptr(math.MaxFloat64), TotalSleep: ptr(math.MaxFloat64), AwakeMinutes: ptr(math.MaxFloat64), Alcohol: ptr(math.MaxFloat64 / 16), Caffeine: ptr(math.MaxFloat64 / 16)}
	result, err := CalculateCSD(extreme)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = json.Marshal(result); err != nil {
		t.Fatalf("finite source cells made an unencodable daily result: %v", err)
	}
	days := []DailyCSD{}
	for i := 0; i < 14; i++ {
		days = append(days, DailyCSD{end.AddDate(0, 0, -i).Format("2006-01-02"), extreme})
	}
	summary := SummarizeCSD(days, end)
	if _, err = json.Marshal(summary); err != nil {
		t.Fatalf("finite source cells made an unencodable summary: %v", err)
	}
	for _, cell := range []CSD{{Alcohol: ptr(math.MaxFloat64)}, {Caffeine: ptr(math.MaxFloat64)}} {
		if _, err = CalculateCSD(cell); err == nil {
			t.Fatal("unrepresentable 14-day sum accepted")
		}
	}
}
