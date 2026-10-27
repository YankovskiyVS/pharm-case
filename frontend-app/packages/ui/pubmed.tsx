import { useEffect, useRef, useState, type FormEvent } from "react";
import { BookOpen, ExternalLink, Search } from "lucide-react";
import { api } from "../api-client";
import { Empty, ErrorBox, Loading } from "./common";
import { navigate, useUrlState } from "./navigation";

type PubMedArticle = {
  pmid: string;
  title: string;
  authors: string[];
  journal: string;
  published_at?: string | null;
  article_types?: string[];
  doi?: string | null;
  abstract?: string | null;
  url: string;
};

type PubMedResult = {
  items: PubMedArticle[];
  total: number;
  next_cursor: string | null;
  truncated?: boolean;
};

const sortLabels = {
  relevance: "По релевантности",
  pub_date: "Сначала новые",
} as const;

export function PubMedResearch() {
  const [urlQuery] = useUrlState("research_q", "");
  const [urlSort] = useUrlState("research_sort", "relevance");
  const [query, setQuery] = useState(urlQuery);
  const [sort, setSort] = useState<keyof typeof sortLabels>(
    urlSort === "pub_date" ? "pub_date" : "relevance",
  );
  const [items, setItems] = useState<PubMedArticle[]>([]);
  const [total, setTotal] = useState(0);
  const [cursor, setCursor] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [searched, setSearched] = useState(false);
  const [error, setError] = useState("");
  const controller = useRef<AbortController | null>(null);

  useEffect(() => () => controller.current?.abort(), []);

  async function search(nextCursor?: string) {
    const value = query.trim();
    if (value.length < 2) {
      setError("Введите запрос длиной не менее двух символов.");
      return;
    }
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    const append = Boolean(nextCursor);
    if (append) setLoadingMore(true);
    else setLoading(true);
    setError("");
    try {
      const result = await api<PubMedResult>(
        "/doctor/pubmed/search",
        {
          query: value,
          sort,
          limit: 10,
          ...(nextCursor ? { cursor: nextCursor } : {}),
        },
        request.signal,
        20000,
      );
      setItems((current) =>
        append ? [...current, ...result.items] : result.items,
      );
      setTotal(result.total);
      setCursor(result.next_cursor);
      setTruncated(Boolean(result.truncated));
      setSearched(true);
      if (!append)
        navigate({
          research_q: value,
          research_sort: sort,
          research_cursor: null,
        });
    } catch (e) {
      if ((e as Error).name !== "AbortError") setError((e as Error).message);
    } finally {
      if (controller.current === request) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setItems([]);
    setCursor(null);
    void search();
  }

  return (
    <section className="panel pubmed-research" aria-label="Поиск PubMed">
      <div className="section-heading">
        <div>
          <h2>
            <BookOpen size={20} /> PubMed
          </h2>
          <p className="helper">
            Поиск выполняется в базе Национальной медицинской библиотеки США.
            Результаты и аннотации предназначены для самостоятельной оценки
            врачом.
          </p>
        </div>
      </div>
      <form className="pubmed-search" onSubmit={submit}>
        <label className="search-field">
          <Search size={18} />
          <input
            aria-label="Запрос в PubMed"
            value={query}
            minLength={2}
            maxLength={500}
            required
            placeholder="Например: insomnia cognitive behavioral therapy"
            onChange={(event) => setQuery(event.target.value)}
          />
        </label>
        <label>
          <span className="sr-only">Сортировка PubMed</span>
          <select
            aria-label="Сортировка PubMed"
            value={sort}
            onChange={(event) =>
              setSort(event.target.value as keyof typeof sortLabels)
            }
          >
            {Object.entries(sortLabels).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <button className="primary" disabled={loading}>
          {loading ? "Ищем…" : "Найти статьи"}
        </button>
      </form>
      <ErrorBox message={error} />
      {loading ? <Loading /> : null}
      {!loading && items.length ? (
        <>
          <p className="muted pubmed-count">
            Найдено: {total.toLocaleString("ru-RU")}
            {truncated ? " · показаны результаты в пределах лимита PubMed" : ""}
          </p>
          <div className="pubmed-results">
            {items.map((article) => (
              <article className="pubmed-article" key={article.pmid}>
                <div className="pubmed-meta">
                  <span>PMID {article.pmid}</span>
                  {article.published_at ? (
                    <span>{article.published_at}</span>
                  ) : null}
                  {article.journal ? <span>{article.journal}</span> : null}
                </div>
                <h3>{article.title}</h3>
                {article.authors.length ? (
                  <p className="muted">{article.authors.join(", ")}</p>
                ) : null}
                <p className="pubmed-abstract">
                  {article.abstract || "Аннотация в PubMed отсутствует."}
                </p>
                <div className="button-row">
                  <a
                    className="text-button"
                    href={article.url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    Открыть в PubMed <ExternalLink size={15} />
                  </a>
                  {article.doi ? (
                    <span className="muted">DOI: {article.doi}</span>
                  ) : null}
                </div>
              </article>
            ))}
          </div>
          {cursor ? (
            <button
              className="secondary pubmed-more"
              disabled={loadingMore}
              onClick={() => void search(cursor)}
            >
              {loadingMore ? "Загружаем…" : "Показать ещё"}
            </button>
          ) : null}
        </>
      ) : !loading && !error && searched ? (
        <Empty>По этому запросу публикации не найдены.</Empty>
      ) : !loading && !error ? (
        <Empty>
          Введите тему, диагноз, метод лечения или поисковое выражение.
        </Empty>
      ) : null}
    </section>
  );
}
