import { Link, Outlet } from 'react-router-dom';
import { Image, MessageCircle, Video } from 'lucide-react';

import { Logo } from '../components/Logo';

export function AuthLayout() {
  return (
    <div className="min-h-full bg-surface-bg text-text-primary">
      <div className="mx-auto flex min-h-screen w-full max-w-6xl flex-col px-5 py-5">
        <header className="flex items-center justify-between border-b border-border-subtle pb-4">
          <Link to="/create/image" className="inline-flex items-center gap-2 text-text-primary">
            <Logo size="sm" />
          </Link>
          <nav className="hidden items-center gap-2 rounded-full bg-surface-2 p-1 text-sm text-text-tertiary sm:flex">
            <Link
              className="inline-flex h-9 items-center gap-2 rounded-full bg-surface-1 px-4 text-text-primary shadow-1"
              to="/create/image"
            >
              <Image size={15} /> 图片
            </Link>
            <Link
              className="inline-flex h-9 items-center gap-2 rounded-full px-4 hover:text-text-primary"
              to="/create/text"
            >
              <MessageCircle size={15} /> 文字
            </Link>
            <Link
              className="inline-flex h-9 items-center gap-2 rounded-full px-4 hover:text-text-primary"
              to="/create/video"
            >
              <Video size={15} /> 视频
            </Link>
          </nav>
        </header>

        <main className="grid flex-1 place-items-center py-10">
          <div className="w-full max-w-[440px] rounded-[28px] border border-border bg-surface-1 p-6 shadow-3">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
