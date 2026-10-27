package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"psitech/internal/scoring"
)

type syntheticPatient struct {
	Name, Pattern, Comment string
	MissingEvery, LastDay  int
}

var syntheticCohort = []syntheticPatient{
	{"Анна Миронова", "stable", "Самочувствие ровное, придерживаюсь режима.", 0, 1},
	{"Михаил Волков", "improving", "Стало легче концентрироваться, сон постепенно восстанавливается.", 0, 1},
	{"Дарья Орлова", "anxiety", "Много рабочих задач, к вечеру усиливается тревога.", 0, 1},
	{"Алексей Лебедев", "deteriorating", "Последние дни настроение заметно снизилось.", 0, 1},
	{"Наталья Кузнецова", "insomnia", "Долго не могу уснуть и часто просыпаюсь.", 0, 1},
	{"Сергей Морозов", "sparse", "Не всегда успеваю заполнить дневник вовремя.", 4, 1},
	{"Екатерина Романова", "recovering", "Возвращаюсь к обычному ритму после сложной недели.", 0, 1},
	{"Илья Новиков", "low_energy", "Утром трудно вставать, энергии мало почти весь день.", 0, 1},
	{"Ольга Васильева", "stable", "Без заметных изменений.", 0, 1},
	{"Роман Фёдоров", "irregular_sleep", "График сна меняется из-за сменной работы.", 0, 1},
	{"Марина Павлова", "new", "Только начинаю вести дневники.", 0, 2},
	{"Денис Никитин", "no_recent", "Несколько дней не заполнял формы.", 0, 5},
	{"Татьяна Белова", "anhedonia", "Мало интереса к привычным занятиям.", 0, 1},
	{"Артём Захаров", "caffeine", "Пью много кофе во второй половине дня.", 0, 1},
	{"Людмила Громова", "good_sleep", "Сон и дневное самочувствие стабильны.", 0, 1},
}

// Seed reconciles the synthetic local cohort without touching non-demo patients.
func (s *Server) Seed(ctx context.Context, password, secret string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx, ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7310626)`); err != nil {
		return err
	}
	encrypted, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var doctor string
	err = tx.QueryRow(ctx, `INSERT INTO users(email,name,role,password_hash,totp_secret)
	 VALUES('doctor@psitech.local','Елена Соколова','doctor',$1,$2)
	 ON CONFLICT(email) DO UPDATE SET name=excluded.name RETURNING id`, string(encrypted), secret).Scan(&doctor)
	if err != nil {
		return err
	}
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return err
	}
	now := time.Now().In(zone)
	emails := make([]string, 0, len(syntheticCohort))
	for i, profile := range syntheticCohort {
		email := fmt.Sprintf("patient%d@psitech.local", i+1)
		if i == 0 {
			email = "patient@psitech.local"
		}
		emails = append(emails, email)
		patient, created, seedErr := seedSyntheticPatient(ctx, tx, doctor, email, profile, string(encrypted), now, i)
		if seedErr != nil {
			return seedErr
		}
		if profile.Pattern == "deteriorating" {
			if err = ensureDeteriorationSeries(ctx, tx, patient, now); err != nil {
				return err
			}
		}
		if s.config.SyntheticDeterioration {
			if _, _, err = createDeteriorationAlert(ctx, tx, patient, now.AddDate(0, 0, -1).Format("2006-01-02")); err != nil {
				return err
			}
		}
		if created && i%3 == 1 {
			_, err = tx.Exec(ctx, `INSERT INTO reviews(doctor_id,patient_id,action,note,created_at)
			 VALUES($1,$2,'reviewed','Плановый просмотр синтетического профиля',$3::timestamptz-make_interval(days=>$4))`, doctor, patient, now, (i%8)+1)
			if err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE care_links c SET active=false FROM users u
	 WHERE c.patient_id=u.id AND c.doctor_id=$1 AND NOT (u.email=ANY($2::text[]))`, doctor, emails)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func seedSyntheticPatient(ctx context.Context, tx pgx.Tx, doctor, email string, profile syntheticPatient, password string, now time.Time, index int) (string, bool, error) {
	var patient string
	err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE email=$1`, email).Scan(&patient)
	created := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !created {
		return "", false, err
	}
	if created {
		err = tx.QueryRow(ctx, `INSERT INTO users(email,name,role,password_hash) VALUES($1,$2,'patient',$3) RETURNING id::text`, email, profile.Name, password).Scan(&patient)
	} else {
		_, err = tx.Exec(ctx, `UPDATE users SET name=$2,active=true WHERE id=$1`, patient, profile.Name)
	}
	if err != nil {
		return "", false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO consents(user_id,version) SELECT $1,'synthetic-seed' WHERE NOT EXISTS(SELECT 1 FROM consents WHERE user_id=$1)`, patient); err != nil {
		return "", false, err
	}
	var link string
	err = tx.QueryRow(ctx, `INSERT INTO care_links(doctor_id,patient_id,active) VALUES($1,$2,true)
	 ON CONFLICT(doctor_id,patient_id) DO UPDATE SET active=true RETURNING id::text`, doctor, patient).Scan(&link)
	if err != nil || !created {
		return patient, false, err
	}
	for _, kind := range []string{"ema", "csd"} {
		due := "09:00"
		if kind == "ema" {
			due = "20:00"
		}
		if _, err = tx.Exec(ctx, `INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by) VALUES($1,$2,$3,$4,$5,$6)`, patient, kind, now.AddDate(0, 0, -14).Format("2006-01-02"), now.AddDate(0, 0, 13).Format("2006-01-02"), due, doctor); err != nil {
			return "", false, err
		}
	}
	for d := 14; d >= profile.LastDay; d-- {
		if profile.MissingEvery > 0 && d%profile.MissingEvery == 0 {
			continue
		}
		day := now.AddDate(0, 0, -d).Format("2006-01-02")
		ema := syntheticEMA(profile, d, index)
		raw, marshalErr := json.Marshal(ema)
		if marshalErr != nil {
			return "", false, marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO diary_entries(patient_id,kind,local_date,timezone,form_id,answers,scores,created_at) VALUES($1,'ema',$2,'Europe/Moscow',gen_random_uuid(),$3,'{}',$2::date+interval '18 hours')`, patient, day, raw); err != nil {
			return "", false, err
		}
		csd := syntheticCSD(profile, d, index)
		result, calcErr := scoring.CalculateCSD(csd)
		if calcErr != nil {
			return "", false, calcErr
		}
		raw, marshalErr = json.Marshal(csd)
		if marshalErr != nil {
			return "", false, marshalErr
		}
		score, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return "", false, marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO diary_entries(patient_id,kind,local_date,timezone,form_id,answers,scores,created_at) VALUES($1,'csd',$2,'Europe/Moscow',gen_random_uuid(),$3,$4,$2::date+interval '6 hours')`, patient, day, raw, score); err != nil {
			return "", false, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO messages(care_link_id,sender_id,body,request_key) VALUES($1,$2,'Здравствуйте! Здесь можно обсудить дневники и самочувствие. Сообщения не заменяют экстренную помощь.',gen_random_uuid())`, link, doctor)
	return patient, true, err
}

func syntheticEMA(profile syntheticPatient, day, index int) scoring.EMA {
	mood, anhedonia, energy, anxiety := 3+(day+index)%2, 3+index%2, 7-index%2, 3+day%2
	switch profile.Pattern {
	case "improving", "recovering":
		mood, energy, anxiety = 3+min(day/4, 3), 8-min(day/5, 2), 3+min(day/5, 2)
	case "anxiety":
		mood, anxiety, energy = 5, 8+day%2, 5
	case "deteriorating":
		if day == 3 {
			mood = 2
		} else if day == 2 {
			mood = 4
		} else if day == 1 {
			mood = 5
		}
	case "low_energy":
		mood, anhedonia, energy = 5, 6, 2+day%2
	case "anhedonia":
		mood, anhedonia, energy = 4, 8, 4
	case "no_recent":
		mood, energy, anxiety = 5, 4, 6
	}
	suicidal := false
	return scoring.EMA{Mood: &mood, Anhedonia: &anhedonia, Energy: &energy, Anxiety: &anxiety, Suicidal: &suicidal, Comment: profile.Comment}
}

func syntheticCSD(profile syntheticPatient, day, index int) scoring.CSD {
	bedtime, attempt, wake, rise := "23:00", "23:15", "07:10", "07:30"
	latency, total, awake := float64(15+day%3*5), float64(420+day%3*10), float64(10+day%2*5)
	awakenings, quality, rested := 1+day%2, 4, 4
	nap, alcohol, caffeine := float64(0), float64(index%3)/2, float64(1+index%3)
	switch profile.Pattern {
	case "insomnia":
		latency, total, awake, awakenings, quality, rested = 65+float64(day%3*5), 300+float64(day%2*20), 70, 4, 2, 2
	case "irregular_sleep":
		if day%2 == 0 {
			bedtime, attempt, wake, rise = "01:30", "01:45", "08:30", "09:00"
		}
		latency, total, awake, quality, rested = 35, 345+float64(day%3*25), 45, 3, 2
	case "low_energy":
		total, quality, rested, nap = 510, 3, 2, 45
	case "caffeine":
		latency, total, caffeine, quality = 45, 360, 5, 3
	case "good_sleep":
		latency, total, awake, awakenings, quality, rested = 10, 455, 5, 0, 5, 5
	}
	return scoring.CSD{Bedtime: bedtime, SleepAttempt: attempt, Latency: &latency, Awakenings: &awakenings, AwakeMinutes: &awake, FinalWake: wake, RiseTime: rise, TotalSleep: &total, Quality: &quality, Rested: &rested, NapMinutes: &nap, Alcohol: &alcohol, Caffeine: &caffeine, Comment: profile.Comment}
}

func ensureDeteriorationSeries(ctx context.Context, tx pgx.Tx, patient string, now time.Time) error {
	for offset, mood := range []int{2, 4, 5} {
		day := now.AddDate(0, 0, offset-3).Format("2006-01-02")
		var current, rawMood string
		err := tx.QueryRow(ctx, `SELECT id::text,answers->>'mood' FROM current_entries WHERE patient_id=$1 AND kind='ema' AND local_date=$2`, patient, day).Scan(&current, &rawMood)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && rawMood == fmt.Sprint(mood) {
			continue
		}
		anhedonia, energy, anxiety, suicidal := 4, 5, 4, false
		answers, _ := json.Marshal(scoring.EMA{Mood: &mood, Anhedonia: &anhedonia, Energy: &energy, Anxiety: &anxiety, Suicidal: &suicidal, Comment: "Синтетическая динамика для демонстрации сигнала"})
		if current == "" {
			_, err = tx.Exec(ctx, `INSERT INTO diary_entries(patient_id,kind,local_date,timezone,form_id,answers,scores,created_at)
			 VALUES($1,'ema',$2,'Europe/Moscow',gen_random_uuid(),$3,'{}',$2::date+interval '20 hours')`, patient, day, answers)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO diary_entries(patient_id,kind,local_date,timezone,form_id,answers,scores,supersedes_id,created_at)
			 VALUES($1,'ema',$2,'Europe/Moscow',gen_random_uuid(),$3,'{}',$4,$2::date+interval '20 hours')`, patient, day, answers, current)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
