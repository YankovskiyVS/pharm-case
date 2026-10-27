// Package scoring implements the supplied CSD and EMA spreadsheet formulas.
package scoring

import (
	"errors"
	"math"
	"time"
)

// EMA contains the five daily responses. Higher energy is better; other scores are worse.
type EMA struct {
	Mood      *int   `json:"mood"`
	Anhedonia *int   `json:"anhedonia"`
	Energy    *int   `json:"energy"`
	Anxiety   *int   `json:"anxiety"`
	Suicidal  *bool  `json:"suicidal"`
	Comment   string `json:"comment"`
}

// Validate rejects missing answers and out-of-range values.
func (e EMA) Validate() error {
	for _, v := range []*int{e.Mood, e.Anhedonia, e.Energy, e.Anxiety} {
		if v == nil || *v < 0 || *v > 10 {
			return errors.New("все четыре оценки должны быть целыми числами от 0 до 10")
		}
	}
	if e.Suicidal == nil {
		return errors.New("ответьте на вопрос о суицидальных мыслях")
	}
	if len([]rune(e.Comment)) > 2000 {
		return errors.New("комментарий не более 2000 символов")
	}
	return nil
}

// CSD preserves empty optional cells rather than interpreting them as zero.
type CSD struct {
	Bedtime          string   `json:"bedtime"`
	SleepAttempt     string   `json:"sleep_attempt"`
	Latency          *float64 `json:"latency"`
	Awakenings       *int     `json:"awakenings"`
	AwakeMinutes     *float64 `json:"awake_minutes"`
	FinalWake        string   `json:"final_wake"`
	AfterWakeMinutes *float64 `json:"after_wake_minutes"`
	EarlyWake        *bool    `json:"early_wake"`
	EarlyMinutes     *float64 `json:"early_minutes"`
	RiseTime         string   `json:"rise_time"`
	TotalSleep       *float64 `json:"total_sleep"`
	Quality          *int     `json:"quality"`
	Rested           *int     `json:"rested"`
	NapMinutes       *float64 `json:"nap_minutes"`
	Alcohol          *float64 `json:"alcohol"`
	Caffeine         *float64 `json:"caffeine"`
	Comment          string   `json:"comment"`
}

// SleepResult uses a ratio for SE and a HH:MM clock for midpoint.
type SleepResult struct {
	TIB      *float64 `json:"tib"`
	SE       *float64 `json:"se"`
	Midpoint *string  `json:"midpoint"`
	WASO     *float64 `json:"waso"`
}

func clock(s string) (float64, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, errors.New("время должно быть в формате ЧЧ:ММ")
	}
	return float64(t.Hour()*60 + t.Minute()), nil
}

// CalculateCSD follows Расчет_качества_сна_CSD.xlsx, CSD_14дней S–V exactly.
// S=MOD(K-B,1)*1440; T=L/S; U=MOD(B+(D+L/2)/1440,1); V=F.
func CalculateCSD(c CSD) (SleepResult, error) {
	result := SleepResult{WASO: c.AwakeMinutes}
	for _, v := range []string{c.Bedtime, c.SleepAttempt, c.FinalWake, c.RiseTime} {
		if v != "" {
			if _, err := clock(v); err != nil {
				return result, err
			}
		}
	}
	for _, v := range []*float64{c.Latency, c.AwakeMinutes, c.AfterWakeMinutes, c.EarlyMinutes, c.TotalSleep, c.NapMinutes, c.Alcohol, c.Caffeine} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return result, errors.New("значения должны быть конечными неотрицательными числами")
		}
	}
	// The workbook sums at most fourteen portion cells. Leave rounding headroom
	// below MaxFloat64 so a valid stored day can never make that JSON sum infinite.
	// This is a floating-point representability guard, not a medical upper limit.
	for _, v := range []*float64{c.Alcohol, c.Caffeine} {
		if v != nil && *v > math.MaxFloat64/16 {
			return result, errors.New("значение слишком велико для числового представления сводки")
		}
	}
	if c.Awakenings != nil && *c.Awakenings < 0 {
		return result, errors.New("некорректное число пробуждений")
	}
	for _, v := range []*int{c.Quality, c.Rested} {
		if v != nil && (*v < 1 || *v > 5) {
			return result, errors.New("оценка сна должна быть от 1 до 5")
		}
	}
	if len([]rune(c.Comment)) > 2000 {
		return result, errors.New("комментарий не более 2000 символов")
	}
	if c.Bedtime != "" && c.RiseTime != "" {
		b, _ := clock(c.Bedtime)
		k, _ := clock(c.RiseTime)
		tib := math.Mod(k-b+1440, 1440)
		result.TIB = &tib
		if c.TotalSleep != nil && tib > 0 {
			se := *c.TotalSleep / tib
			result.SE = &se
		}
	}
	if c.Bedtime != "" && c.Latency != nil && c.TotalSleep != nil {
		b, _ := clock(c.Bedtime)
		minute := int(math.Mod(b+math.Mod(*c.Latency, 1440)+math.Mod(*c.TotalSleep/2, 1440), 1440))
		midpoint := time.Date(2000, 1, 1, minute/60, minute%60, 0, 0, time.UTC).Format("15:04")
		result.Midpoint = &midpoint
	}
	return result, nil
}

// DailyEMA supplies the local date used by the three-day spreadsheet window.
type DailyEMA struct {
	Date    string
	Answers EMA
}

// Summary mirrors EMA_дневник_настроения_3дня.xlsx B7:B15 and exposes completeness.
type Summary struct {
	Count        int      `json:"count"`
	Complete     bool     `json:"complete"`
	Mood         *float64 `json:"mood"`
	Anhedonia    *float64 `json:"anhedonia"`
	Energy       *float64 `json:"energy"`
	Anxiety      *float64 `json:"anxiety"`
	SuicidalDays int      `json:"suicidal_days"`
	MoodDelta    *int     `json:"mood_delta"`
	MaxAnxiety   *int     `json:"max_anxiety"`
	MinEnergy    *int     `json:"min_energy"`
}

// SummarizeEMA averages filled values and calculates day 3 minus day 1, never adjacent substitutes.
func SummarizeEMA(days []DailyEMA, end time.Time) Summary {
	s := Summary{}
	sums := [4]float64{}
	var first, last *int
	start := end.AddDate(0, 0, -2).Format("2006-01-02")
	finish := end.Format("2006-01-02")
	seen := map[string]bool{}
	for _, day := range days {
		if day.Date < start || day.Date > finish || seen[day.Date] {
			continue
		}
		seen[day.Date] = true
		e := day.Answers
		if e.Validate() != nil {
			continue
		}
		s.Count++
		for i, v := range []*int{e.Mood, e.Anhedonia, e.Energy, e.Anxiety} {
			sums[i] += float64(*v)
		}
		if *e.Suicidal {
			s.SuicidalDays++
		}
		if s.MaxAnxiety == nil || *e.Anxiety > *s.MaxAnxiety {
			v := *e.Anxiety
			s.MaxAnxiety = &v
		}
		if s.MinEnergy == nil || *e.Energy < *s.MinEnergy {
			v := *e.Energy
			s.MinEnergy = &v
		}
		if day.Date == start {
			first = e.Mood
		}
		if day.Date == finish {
			last = e.Mood
		}
	}
	s.Complete = s.Count == 3
	if s.Count > 0 {
		m := sums[0] / float64(s.Count)
		a := sums[1] / float64(s.Count)
		e := sums[2] / float64(s.Count)
		x := sums[3] / float64(s.Count)
		s.Mood = &m
		s.Anhedonia = &a
		s.Energy = &e
		s.Anxiety = &x
	}
	if first != nil && last != nil {
		v := *last - *first
		s.MoodDelta = &v
	}
	return s
}
