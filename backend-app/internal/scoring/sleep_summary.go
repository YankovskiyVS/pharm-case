package scoring

import "time"

// DailyCSD supplies the local date and answers of one current CSD revision.
type DailyCSD struct {
	Date    string
	Answers CSD
}

// SleepSummary mirrors Расчет_качества_сна_CSD.xlsx, Сводка B3:B12.
// Averages ignore missing cells; SE remains a ratio; empty SUM ranges yield zero.
// Samples distinguishes a genuine zero result from an entirely missing column.
type SleepSummary struct {
	Count          int            `json:"count"`
	Complete       bool           `json:"complete"`
	From           string         `json:"from"`
	To             string         `json:"to"`
	MeanLatency    *float64       `json:"mean_latency"`
	MeanAwakenings *float64       `json:"mean_awakenings"`
	MeanWASO       *float64       `json:"mean_waso"`
	MeanTotalSleep *float64       `json:"mean_total_sleep"`
	MeanSE         *float64       `json:"mean_se"`
	MeanQuality    *float64       `json:"mean_quality"`
	MeanRested     *float64       `json:"mean_rested"`
	MeanNapMinutes *float64       `json:"mean_nap_minutes"`
	TotalAlcohol   float64        `json:"total_alcohol"`
	TotalCaffeine  float64        `json:"total_caffeine"`
	Samples        map[string]int `json:"samples"`
}

type average struct {
	value float64
	count int
}

func (a *average) add(value *float64) {
	if value == nil {
		return
	}
	a.count++
	// Incremental mean avoids overflowing an intermediate sum of valid positive cells.
	a.value += (*value - a.value) / float64(a.count)
}
func (a average) result() *float64 {
	if a.count == 0 {
		return nil
	}
	value := a.value
	return &value
}
func numeric(value *int) *float64 {
	if value == nil {
		return nil
	}
	v := float64(*value)
	return &v
}

// SummarizeCSD applies the supplied fourteen-day summary, ending on end's local date.
// It accepts the current revision for each date, excludes other dates and deduplicates
// days in input order. No diagnostic thresholds or inferred answers are introduced.
func SummarizeCSD(days []DailyCSD, end time.Time) SleepSummary {
	summary := SleepSummary{From: end.AddDate(0, 0, -13).Format("2006-01-02"), To: end.Format("2006-01-02"), Samples: map[string]int{}}
	means := map[string]*average{}
	for _, key := range []string{"latency", "awakenings", "waso", "total_sleep", "se", "quality", "rested", "nap_minutes"} {
		means[key] = &average{}
		summary.Samples[key] = 0
	}
	summary.Samples["alcohol"] = 0
	summary.Samples["caffeine"] = 0
	seen := map[string]bool{}
	for _, day := range days {
		parsed, err := time.Parse("2006-01-02", day.Date)
		if err != nil || parsed.Format("2006-01-02") != day.Date || day.Date < summary.From || day.Date > summary.To || seen[day.Date] {
			continue
		}
		score, err := CalculateCSD(day.Answers)
		if err != nil {
			continue
		}
		seen[day.Date] = true
		summary.Count++
		c := day.Answers
		values := map[string]*float64{"latency": c.Latency, "awakenings": numeric(c.Awakenings), "waso": score.WASO, "total_sleep": c.TotalSleep, "se": score.SE, "quality": numeric(c.Quality), "rested": numeric(c.Rested), "nap_minutes": c.NapMinutes}
		for key, value := range values {
			means[key].add(value)
		}
		if c.Alcohol != nil {
			summary.TotalAlcohol += *c.Alcohol
			summary.Samples["alcohol"]++
		}
		if c.Caffeine != nil {
			summary.TotalCaffeine += *c.Caffeine
			summary.Samples["caffeine"]++
		}
	}
	summary.Complete = summary.Count == 14
	for key, mean := range means {
		summary.Samples[key] = mean.count
	}
	summary.MeanLatency = means["latency"].result()
	summary.MeanAwakenings = means["awakenings"].result()
	summary.MeanWASO = means["waso"].result()
	summary.MeanTotalSleep = means["total_sleep"].result()
	summary.MeanSE = means["se"].result()
	summary.MeanQuality = means["quality"].result()
	summary.MeanRested = means["rested"].result()
	summary.MeanNapMinutes = means["nap_minutes"].result()
	return summary
}
