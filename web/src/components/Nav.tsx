import { useEffect, useState } from 'react';
import { navigate } from '../router';
import { listReview } from '../api';
import './Nav.css';

interface Props {
  currentPath: string;
}

const LINKS = [
  { label: 'Sessions', href: '/sessions' },
  { label: 'Agents', href: '/agents' },
  { label: 'Queues', href: '/queues' },
  { label: 'Review', href: '/review' },
];

export function Nav({ currentPath }: Props) {
  const [reviewCount, setReviewCount] = useState(0);

  useEffect(() => {
    let active = true;
    const poll = async () => {
      try {
        const items = await listReview();
        if (active) setReviewCount(items.length);
      } catch {
        // ignore
      }
    };
    poll();
    const t = setInterval(poll, 5000);
    return () => { active = false; clearInterval(t); };
  }, []);

  return (
    <nav className="nav">
      <span className="nav-brand" onClick={() => navigate('/sessions')}>agentq</span>
      <ul className="nav-links">
        {LINKS.map(({ label, href }) => {
          const active = currentPath === href || currentPath.startsWith(href + '/');
          const badge = label === 'Review' && reviewCount > 0
            ? <span className="nav-badge">{reviewCount}</span>
            : null;
          return (
            <li key={href}>
              <a
                href={`#${href}`}
                className={active ? 'active' : ''}
                onClick={e => { e.preventDefault(); navigate(href); }}
              >
                {label}{badge}
              </a>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
