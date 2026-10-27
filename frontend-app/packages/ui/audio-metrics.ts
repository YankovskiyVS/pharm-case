export const metricLabels: Record<string, [string, string, number]> = {
  activity_contrast_db: ["Контраст речевых и неречевых участков", "дБ", 1],
  duration_seconds: ["Длительность", "с", 1],
  active_seconds: ["Речевая активность / фонация", "с", 1],
  pause_ratio: ["Доля неактивных участков", "%", 100],
  internal_pause_mean_seconds: ["Средняя внутренняя пауза", "с", 1],
  words_per_minute: ["Темп по расшифровке", "слов/мин", 1],
  f0_median_hz: ["Медиана основного тона", "Гц", 1],
  f0_sd_hz: ["Вариативность основного тона", "Гц", 1],
  level_dbfs: ["Уровень цифрового сигнала", "dBFS", 1],
  hnr_db: ["Гармоники / шум", "дБ", 1],
  jitter_local: ["Нестабильность периода (jitter)", "%", 100],
  shimmer_local: ["Нестабильность амплитуды (shimmer)", "%", 100],
};
