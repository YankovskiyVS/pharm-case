import { test, expect, type Page } from "@playwright/test";

const doctor = {
  id: "10000000-0000-4000-8000-000000000001",
  name: "Тестовый Врач",
  email: "doctor@local.test",
  role: "doctor",
  timezone: "Europe/Moscow",
};
const patient = {
  ...doctor,
  id: "20000000-0000-4000-8000-000000000001",
  role: "patient",
  name: "Анна Тестова",
  email: "patient@local.test",
};
const today = "2026-10-26";
const deterioration = {
  id: "40000000-0000-4000-8000-000000000001",
  patient_id: patient.id,
  patient_name: patient.name,
  type: "DETERIORATION",
  severity: "warning",
  status: "new",
  created_at: "2026-10-26T18:00:00Z",
  overdue: false,
  metric: "mood",
  baseline_value: 2,
  current_value: 5,
  delta: 3,
  period_start: "2026-10-24",
  period_end: "2026-10-26",
  rule_version: "ema-mood-delta-v1",
};
const detail = {
  patient,
  entries: [],
  alerts: [deterioration],
  assignments: [],
  reviews: [],
  care_team: [],
  summary: {
    count: 3,
    complete: true,
    mood: 3.7,
    anhedonia: 2,
    energy: 5,
    anxiety: 4,
    suicidal_days: 0,
    mood_delta: 3,
    max_anxiety: 5,
    min_energy: 4,
  },
  sleep_summary: {
    count: 0,
    complete: false,
    from: "2026-10-13",
    to: today,
    samples: {},
    total_alcohol: 0,
    total_caffeine: 0,
  },
  today,
  review_snapshot: "a".repeat(64),
};

async function fixture(page: Page) {
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { user: doctor, csrf: "csrf" } }),
  );
  await page.route("**/api/v1/events", (route) => route.abort());
  await page.route("**/api/v1/notifications?*", (route) =>
    route.fulfill({
      json: { items: [], total: 0, unread_count: 0, next_cursor: null },
    }),
  );
  await page.route("**/api/v1/dashboard", (route) =>
    route.fulfill({
      json: {
        patients: [
          {
            id: patient.id,
            name: patient.name,
            email: patient.email,
            open_alerts: 1,
            ema_count: 3,
            last_entry: "2026-10-26T18:00:00Z",
            reviewed_at: null,
          },
        ],
        alerts: [deterioration],
        alerts_total: 1,
        patients_total: 1,
        pending_reviews_total: 1,
      },
    }),
  );
  await page.route(`**/api/v1/patients/${patient.id}`, (route) =>
    route.fulfill({ json: detail }),
  );
  await page.route("**/api/v1/doctor/alerts?*", (route) =>
    route.fulfill({
      json: { items: [deterioration], total: 1, next_cursor: null },
    }),
  );
  await page.route("**/api/v1/patients/*/alerts?*", (route) =>
    route.fulfill({
      json: { items: [deterioration], total: 1, next_cursor: null },
    }),
  );
  await page.route("**/api/v1/patients/*/reviews?*", (route) =>
    route.fulfill({ json: { items: [], total: 0, next_cursor: null } }),
  );
}

test("doctor sees explainable deterioration alert", async ({ page }) => {
  await fixture(page);
  await page.goto("/doctor/?page=alerts");
  await expect(
    page.getByText(
      "Ухудшение динамики настроения по EMA: 2 → 5 (изменение +3)",
    ),
  ).toBeVisible();
  await expect(
    page.getByText(/Требуется оценка врача; индикатор не является диагнозом/),
  ).toBeVisible();
});

test("doctor downloads daily diary CSV from patient header", async ({
  page,
}) => {
  await fixture(page);
  let requested = false;
  await page.route("**/api/v1/patients/*/export?format=csv", (route) => {
    requested = true;
    return route.fulfill({
      status: 200,
      headers: { "content-type": "text/csv; charset=utf-8" },
      body: "date,form,mood\n2026-10-26,ema,5\n",
    });
  });
  await page.goto(`/doctor/?patient=${patient.id}&tab=overview`);
  await page.getByRole("button", { name: "Скачать дневники CSV" }).click();
  await expect.poll(() => requested).toBe(true);
});

test("PubMed section shows abstract and loads the next page", async ({
  page,
}) => {
  await fixture(page);
  const requests: unknown[] = [];
  await page.route("**/api/v1/doctor/pubmed/search", async (route) => {
    const body = route.request().postDataJSON();
    requests.push(body);
    const second = body.cursor === "next-10";
    await route.fulfill({
      json: {
        items: [
          {
            pmid: second ? "222" : "111",
            title: second ? "Follow-up article" : "CBT for insomnia",
            authors: ["Ivanov I", "Smith J"],
            journal: "Sleep Medicine",
            published_at: "2026",
            article_types: ["Clinical Trial"],
            doi: "10.1000/test",
            abstract: second
              ? "Second abstract."
              : "A structured abstract returned by PubMed.",
            url: `https://pubmed.ncbi.nlm.nih.gov/${second ? "222" : "111"}/`,
          },
        ],
        total: 11,
        next_cursor: second ? null : "next-10",
        truncated: false,
      },
    });
  });
  await page.goto("/doctor/?page=research");
  await page.getByLabel("Запрос в PubMed").fill("insomnia CBT");
  await page.getByRole("button", { name: "Найти статьи" }).click();
  await expect(
    page.getByText("A structured abstract returned by PubMed."),
  ).toBeVisible();
  const link = page.getByRole("link", { name: /Открыть в PubMed/ }).first();
  await expect(link).toHaveAttribute("target", "_blank");
  await expect(link).toHaveAttribute("rel", "noopener noreferrer");
  await page.getByRole("button", { name: "Показать ещё" }).click();
  await expect(page.getByText("Second abstract.")).toBeVisible();
  expect(requests).toEqual([
    { query: "insomnia CBT", sort: "relevance", limit: 10 },
    {
      query: "insomnia CBT",
      sort: "relevance",
      limit: 10,
      cursor: "next-10",
    },
  ]);
});
