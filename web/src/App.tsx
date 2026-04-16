import { useRoute, matchRoute, navigate } from './router';
import { Nav } from './components/Nav';
import { Sessions } from './pages/Sessions';
import { SessionDetail } from './pages/SessionDetail';
import { Review } from './pages/Review';
import { Agents } from './pages/Agents';
import { Queues } from './pages/Queues';
import { useEffect } from 'react';

export default function App() {
  const { path } = useRoute();

  // Redirect root to /sessions
  useEffect(() => {
    if (path === '/') navigate('/sessions');
  }, [path]);

  let page: React.ReactNode = null;

  const sessionDetail = matchRoute(path, '/sessions/:id');
  if (sessionDetail) {
    page = <SessionDetail id={sessionDetail.id} />;
  } else if (path === '/sessions') {
    page = <Sessions />;
  } else if (path === '/review') {
    page = <Review />;
  } else if (path === '/agents') {
    page = <Agents />;
  } else if (path === '/queues') {
    page = <Queues />;
  }

  return (
    <div className="app">
      <Nav currentPath={path} />
      <main className="app-main">
        {page}
      </main>
    </div>
  );
}
