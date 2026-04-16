import './StatusBadge.css';

interface Props {
  status: string;
}

const STATUS_LABELS: Record<string, string> = {
  pending: 'pending',
  in_progress: 'running',
  awaiting_review: 'review',
  completed: 'done',
  failed: 'failed',
};

export function StatusBadge({ status }: Props) {
  const label = STATUS_LABELS[status] ?? status;
  return <span className={`badge badge-${status.replace('_', '-')}`}>{label}</span>;
}
