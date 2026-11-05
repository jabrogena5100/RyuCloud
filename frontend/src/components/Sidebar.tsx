"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ComponentType, SVGProps } from "react";
import { HistoryIcon, HomeIcon, LibraryIcon, StudioIcon, SubscriptionsIcon, TrendingIcon, UploadIcon } from "./icons";

type NavItem = {
  label: string;
  icon: ComponentType<SVGProps<SVGSVGElement>>;
  href?: string; // items without href are planned for later phases
};

const NAV_ITEMS: NavItem[] = [
  { label: "Home", icon: HomeIcon, href: "/" },
  { label: "Upload", icon: UploadIcon, href: "/upload" },
  { label: "Your videos", icon: StudioIcon, href: "/studio" },
  { label: "Trending", icon: TrendingIcon },
  { label: "Subscriptions", icon: SubscriptionsIcon },
  { label: "Library", icon: LibraryIcon },
  { label: "History", icon: HistoryIcon },
];

function NavList({ compact }: { compact: boolean }) {
  const pathname = usePathname();
  const base = compact
    ? "flex flex-col items-center gap-1 rounded-lg px-1 py-3 text-[10px]"
    : "flex items-center gap-5 rounded-lg px-3 py-2 text-sm";

  return (
    <ul className="flex flex-col gap-0.5">
      {NAV_ITEMS.map(({ label, icon: Icon, href }) => (
        <li key={label}>
          {href ? (
            <Link
              href={href}
              aria-current={pathname === href ? "page" : undefined}
              className={`${base} hover:bg-neutral-100 dark:hover:bg-neutral-800 ${
                pathname === href ? "bg-neutral-100 font-semibold dark:bg-neutral-800" : ""
              }`}
            >
              <Icon />
              <span>{label}</span>
            </Link>
          ) : (
            <span
              aria-disabled="true"
              title="Coming in a later phase"
              className={`${base} cursor-not-allowed text-neutral-400 dark:text-neutral-600`}
            >
              <Icon />
              <span>{label}</span>
            </span>
          )}
        </li>
      ))}
    </ul>
  );
}

type SidebarProps = {
  collapsed: boolean;
  drawerOpen: boolean;
  onClose: () => void;
};

export function Sidebar({ collapsed, drawerOpen, onClose }: SidebarProps) {
  return (
    <>
      {/* Desktop: persistent sidebar that collapses to an icon rail. */}
      <nav
        aria-label="Primary"
        className={`sticky top-14 hidden h-[calc(100vh-3.5rem)] shrink-0 overflow-y-auto p-2 lg:block ${
          collapsed ? "w-20" : "w-60"
        }`}
      >
        <NavList compact={collapsed} />
        {!collapsed && (
          <p className="mt-4 border-t border-neutral-200 px-3 pt-4 text-xs text-neutral-500 dark:border-neutral-800">
            RyuCloud · Phase 2 preview
          </p>
        )}
      </nav>

      {/* Mobile/tablet: overlay drawer. */}
      {drawerOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <button
            type="button"
            aria-label="Close navigation"
            onClick={onClose}
            className="absolute inset-0 bg-black/50"
          />
          <nav
            aria-label="Primary"
            className="absolute inset-y-0 left-0 w-64 overflow-y-auto bg-white p-2 pt-16 shadow-xl dark:bg-neutral-950"
          >
            <NavList compact={false} />
          </nav>
        </div>
      )}
    </>
  );
}
