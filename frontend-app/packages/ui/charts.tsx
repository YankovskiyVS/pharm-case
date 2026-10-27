import { useState } from "react";
import type { Entry } from "../api-client";
function shortDate(date: string) {
  return new Date(date.slice(0, 10) + "T12:00:00").toLocaleDateString("ru-RU", {
    day: "numeric",
    month: "short",
  });
}
function dayNumber(date: string) {
  return Date.parse(date.slice(0, 10) + "T00:00:00Z") / 86400000;
}
type Point = {
  id: string;
  date: string;
  values: Record<string, number | null>;
};
function CalendarChart({
  data,
  series,
  label,
}: {
  data: Point[];
  series: { key: string; label: string; color: string }[];
  label: string;
}) {
  if (data.length < 2)
    return <p className="empty">График появится после двух записей.</p>;
  const first = dayNumber(data[0].date),
    last = dayNumber(data[data.length - 1].date),
    span = Math.max(1, last - first);
  const values = data.flatMap((d) =>
    series.flatMap((s) => (d.values[s.key] == null ? [] : [d.values[s.key]!])),
  );
  const max = Math.max(10, ...values),
    min = Math.min(0, ...values);
  const y = (n: number) => 190 - ((n - min) / Math.max(1, max - min)) * 150;
  const x = (date: string) => 40 + ((dayNumber(date) - first) / span) * 620;
  function segments(key: string) {
    const groups: string[][] = [];
    let previous = -Infinity;
    let group: string[] = [];
    for (const d of data) {
      const value = d.values[key];
      const day = dayNumber(d.date);
      if (value == null || day - previous > 1) {
        if (group.length) groups.push(group);
        group = [];
      }
      if (value != null) {
        group.push(`${x(d.date)},${y(value)}`);
        previous = day;
      } else previous = -Infinity;
    }
    if (group.length) groups.push(group);
    return groups;
  }
  return (
    <div className="chart">
      <svg viewBox="0 0 700 235" role="img" aria-label={label}>
        <title>{label}. Пропущенные дни показаны разрывами.</title>
        {[min, (min + max) / 2, max].map((n) => (
          <g key={n}>
            <line
              x1="40"
              x2="660"
              y1={y(n)}
              y2={y(n)}
              stroke="#dce6eb"
              strokeDasharray="4 5"
            />
            <text x="10" y={y(n) + 4}>
              {Math.round(n)}
            </text>
          </g>
        ))}
        {series.map((s) => (
          <g key={s.key}>
            {segments(s.key).map((points, i) => (
              <polyline
                key={i}
                points={points.join(" ")}
                fill="none"
                stroke={s.color}
                strokeWidth="2.5"
              />
            ))}
            {data
              .filter((d) => d.values[s.key] != null)
              .map((d) => (
                <circle
                  key={d.id}
                  cx={x(d.date)}
                  cy={y(d.values[s.key]!)}
                  r="3"
                  fill={s.color}
                >
                  <title>
                    {shortDate(d.date)}: {d.values[s.key]}
                  </title>
                </circle>
              ))}
          </g>
        ))}
        {[data[0], data[data.length - 1]].map((d) => (
          <text key={d.id} x={x(d.date)} y="222" textAnchor="middle">
            {shortDate(d.date)}
          </text>
        ))}
      </svg>
      <div className="chart-legend">
        {series.map((s) => (
          <span key={s.key}>
            <i style={{ background: s.color }} />
            {s.label}
          </span>
        ))}
      </div>
      <small className="muted">
        Календарная шкала. Линии прерываются в дни без записи.
      </small>
      <details className="chart-data">
        <summary>Таблица значений</summary>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Дата</th>
                {series.map((s) => (
                  <th key={s.key}>{s.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.map((d) => (
                <tr key={d.id}>
                  <td>{shortDate(d.date)}</td>
                  {series.map((s) => (
                    <td key={s.key}>
                      {d.values[s.key]?.toLocaleString("ru-RU", {
                        maximumFractionDigits: 1,
                      }) ?? "—"}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </div>
  );
}
export function TrendChart({ entries }: { entries: Entry[] }) {
  const data = entries
    .filter((e) => e.kind === "ema")
    .slice(0, 14)
    .reverse()
    .map((e) => ({
      id: e.id,
      date: e.local_date,
      values: e.answers as unknown as Record<string, number | null>,
    }));
  return (
    <>
      <CalendarChart
        data={data}
        series={[
          { key: "mood", label: "Настроение", color: "#126b78" },
          { key: "energy", label: "Энергия", color: "#b0873c" },
          { key: "anxiety", label: "Тревога", color: "#9e749c" },
        ]}
        label="Динамика настроения, энергии и тревоги по заполненным дневникам"
      />
      <p className="helper">
        Шкала 0–10. Выше настроение/тревога — хуже; выше энергия — лучше.
      </p>
    </>
  );
}
export function SleepTrendChart({ entries }: { entries: Entry[] }) {
  const [metric, setMetric] = useState("total_sleep");
  const labels: Record<string, string> = {
    total_sleep: "Общее время сна, мин",
    se: "Эффективность сна, %",
    waso: "Бодрствование ночью, мин",
  };
  const data = entries
    .filter((e) => e.kind === "csd")
    .slice(0, 30)
    .reverse()
    .map((e) => {
      const score = e.scores as Record<string, number | null>;
      const answers = e.answers as unknown as Record<string, number | null>;
      return {
        id: e.id,
        date: e.local_date,
        values: {
          total_sleep: answers.total_sleep,
          se: score.se == null ? null : score.se * 100,
          waso: score.waso,
        },
      };
    });
  return (
    <section className="sleep-trend">
      <div className="section-heading">
        <h2>Динамика сна</h2>
        <select
          aria-label="Показатель сна на графике"
          value={metric}
          onChange={(e) => setMetric(e.target.value)}
        >
          {Object.entries(labels).map(([key, label]) => (
            <option key={key} value={key}>
              {label}
            </option>
          ))}
        </select>
      </div>
      <CalendarChart
        data={data}
        series={[{ key: metric, label: labels[metric], color: "#65759b" }]}
        label="Динамика показателей сна"
      />
    </section>
  );
}
