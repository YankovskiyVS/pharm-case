package app

import (
	"context"
	"testing"
)

func TestSyntheticCohortIsDiverseAndIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var patients, names, comments, diaries int
	err := f.db.QueryRow(ctx, `SELECT count(*),count(DISTINCT u.name)
	 FROM users u JOIN care_links c ON c.patient_id=u.id
	 WHERE c.doctor_id=$1 AND c.active AND u.active AND u.email LIKE 'patient%@psitech.local'`, f.doctor.ID).Scan(&patients, &names)
	if err != nil {
		t.Fatal(err)
	}
	if patients != 15 || names != 15 {
		t.Fatalf("expected fifteen distinct demo patients, got patients=%d names=%d", patients, names)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(DISTINCT answers->>'comment'),count(*) FROM current_entries e
	 JOIN care_links c ON c.patient_id=e.patient_id WHERE c.doctor_id=$1`, f.doctor.ID).Scan(&comments, &diaries); err != nil {
		t.Fatal(err)
	}
	if comments < 12 {
		t.Fatalf("synthetic cohort is not diverse enough: %d distinct narratives", comments)
	}
	before := diaries
	if err = f.server.Seed(ctx, "integration-password-123", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM current_entries e
	 JOIN care_links c ON c.patient_id=e.patient_id WHERE c.doctor_id=$1`, f.doctor.ID).Scan(&diaries); err != nil {
		t.Fatal(err)
	}
	if diaries != before {
		t.Fatalf("repeated seed changed current diary count: before=%d after=%d", before, diaries)
	}
}
