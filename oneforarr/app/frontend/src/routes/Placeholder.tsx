// Pages that come in later milestones show what they'll hold.

type Props = { title: string; text: string };

export function Placeholder({ title, text }: Props) {
  return (
    <>
      <header className="page-head">
        <h1>{title}</h1>
      </header>
      <div className="card empty">
        <p>{text}</p>
        <p className="muted">Coming in a later step.</p>
      </div>
    </>
  );
}
