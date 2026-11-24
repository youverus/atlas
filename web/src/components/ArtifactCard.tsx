import type { ArtifactRef } from "../api";
import { artifactKindLabel, formatArtifactBytes } from "../lib/artifacts";

function ArtifactKindIcon({ kind }: { kind: string }) {
  switch (kind) {
    case "html":
      return (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="3" y="4" width="18" height="16" rx="2.5" />
          <path d="M3 9h18" />
          <path d="m10 13-2 2 2 2" />
          <path d="m14 13 2 2-2 2" />
        </svg>
      );
    case "svg":
      return (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="3" y="3" width="18" height="18" rx="2.5" />
          <circle cx="9" cy="9" r="1.5" />
          <path d="m21 15-4.5-4.5a2 2 0 0 0-2.8 0L6 18" />
        </svg>
      );
    case "csv":
      return (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="3" y="4" width="18" height="16" rx="2.5" />
          <path d="M3 10h18" />
          <path d="M3 15h18" />
          <path d="M9 4v16" />
        </svg>
      );
    case "markdown":
      return (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
          <path d="M14 3v5h5" />
          <path d="M9 13h6" />
          <path d="M9 17h4" />
        </svg>
      );
    default:
      return (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="m8 9-4 3 4 3" />
          <path d="m16 9 4 3-4 3" />
          <path d="m13 6-2 12" />
        </svg>
      );
  }
}

export function ArtifactCard({
  artifact,
  active = false,
  onOpen,
}: {
  artifact: ArtifactRef;
  active?: boolean;
  onOpen: (id: string, version?: number) => void;
}) {
  const kindLabel = artifactKindLabel(artifact.kind, artifact.language);
  const byteLabel = formatArtifactBytes(artifact.bytes);
  const metaParts = [kindLabel, `v${artifact.version}`, byteLabel].filter(Boolean);

  return (
    <button
      type="button"
      className="artifact-card"
      data-active={active ? "true" : undefined}
      onClick={() => onOpen(artifact.id, artifact.version)}
      title={`查看产物：${artifact.title || artifact.id} (v${artifact.version})`}
    >
      <span className="artifact-card__icon">
        <ArtifactKindIcon kind={artifact.kind} />
      </span>
      <span className="artifact-card__body">
        <span className="artifact-card__title-row">
          <span className="artifact-card__title">{artifact.title || artifact.id}</span>
          <span className="artifact-card__badge">{metaParts.join(" · ")}</span>
        </span>
        {artifact.description ? (
          <span className="artifact-card__desc">{artifact.description}</span>
        ) : null}
      </span>
      <span className="artifact-card__cta">{active ? "预览中" : "查看"}</span>
    </button>
  );
}

export function ArtifactCards({
  artifacts,
  activeId,
  onOpen,
}: {
  artifacts: ArtifactRef[];
  activeId?: string;
  onOpen: (id: string, version?: number) => void;
}) {
  if (artifacts.length === 0) return null;
  return (
    <div className="artifact-cards" aria-label="本轮生成的产物">
      {artifacts.map((item) => (
        <ArtifactCard
          key={item.id}
          artifact={item}
          active={activeId === item.id}
          onOpen={onOpen}
        />
      ))}
    </div>
  );
}
