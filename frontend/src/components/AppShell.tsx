"use client";

import { usePathname } from "next/navigation";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Navbar } from "./Navbar";
import { Sidebar } from "./Sidebar";

const DESKTOP_QUERY = "(min-width: 1024px)"; // Tailwind's lg breakpoint

export function AppShell({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const pathname = usePathname();

  // Close the mobile drawer after navigating.
  useEffect(() => {
    setDrawerOpen(false);
  }, [pathname]);

  const toggleNavigation = useCallback(() => {
    if (window.matchMedia(DESKTOP_QUERY).matches) {
      setCollapsed((value) => !value);
    } else {
      setDrawerOpen((value) => !value);
    }
  }, []);

  return (
    <div className="min-h-screen">
      <Navbar onMenuClick={toggleNavigation} />
      <div className="flex">
        <Sidebar collapsed={collapsed} drawerOpen={drawerOpen} onClose={() => setDrawerOpen(false)} />
        <main className="min-w-0 flex-1 px-4 py-6 sm:px-6">{children}</main>
      </div>
    </div>
  );
}
