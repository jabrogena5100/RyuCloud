"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth-context";
import { LogoMark, MenuIcon, SearchIcon, UploadIcon } from "./icons";

export function Navbar({ onMenuClick }: { onMenuClick: () => void }) {
  const { user, status, logout } = useAuth();
  const router = useRouter();

  async function handleSignOut() {
    await logout();
    router.push("/");
  }

  return (
    <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-4 border-b border-neutral-200 bg-white/95 px-2 backdrop-blur sm:px-4 dark:border-neutral-800 dark:bg-neutral-950/95">
      <div className="flex shrink-0 items-center gap-2">
        <button
          type="button"
          onClick={onMenuClick}
          aria-label="Toggle navigation"
          className="rounded-full p-2 hover:bg-neutral-100 dark:hover:bg-neutral-800"
        >
          <MenuIcon />
        </button>
        <Link href="/" className="flex items-center gap-1.5 text-lg font-semibold tracking-tight">
          <LogoMark />
          <span className="hidden sm:inline">RyuCloud</span>
        </Link>
      </div>

      {/* Search is a layout placeholder until a search API exists. */}
      <form role="search" onSubmit={(e) => e.preventDefault()} className="hidden max-w-xl flex-1 md:flex">
        <input
          type="search"
          placeholder="Search (coming soon)"
          aria-label="Search videos"
          className="w-full rounded-l-full border border-neutral-300 bg-transparent px-4 py-1.5 outline-none focus:border-blue-500 dark:border-neutral-700"
        />
        <button
          type="submit"
          aria-label="Search"
          className="rounded-r-full border border-l-0 border-neutral-300 bg-neutral-100 px-5 hover:bg-neutral-200 dark:border-neutral-700 dark:bg-neutral-800 dark:hover:bg-neutral-700"
        >
          <SearchIcon className="h-5 w-5" />
        </button>
      </form>

      <div className="flex shrink-0 items-center gap-2">
        {status === "loading" ? (
          <div className="h-8 w-24 animate-pulse rounded-full bg-neutral-200 dark:bg-neutral-800" />
        ) : user ? (
          <>
            <Link
              href="/upload"
              className="flex items-center gap-1.5 rounded-full border border-neutral-300 px-3 py-1.5 text-sm font-medium hover:bg-neutral-100 dark:border-neutral-700 dark:hover:bg-neutral-800"
            >
              <UploadIcon className="h-5 w-5" />
              <span className="hidden sm:inline">Upload</span>
            </Link>
            <span
              title={user.email}
              className="flex h-8 w-8 items-center justify-center rounded-full bg-blue-600 text-sm font-semibold text-white"
            >
              {user.display_name.charAt(0).toUpperCase()}
            </span>
            <button
              type="button"
              onClick={handleSignOut}
              className="rounded-full px-3 py-1.5 text-sm font-medium hover:bg-neutral-100 dark:hover:bg-neutral-800"
            >
              Sign out
            </button>
          </>
        ) : (
          <Link
            href="/login"
            className="rounded-full border border-neutral-300 px-3 py-1.5 text-sm font-medium text-blue-600 hover:bg-blue-50 dark:border-neutral-700 dark:text-blue-400 dark:hover:bg-neutral-800"
          >
            Sign in
          </Link>
        )}
      </div>
    </header>
  );
}
