import Link from "next/link";

export default function NotFound() {
  return (
    <div className="mx-auto mt-16 max-w-md text-center">
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <p className="mt-2 text-neutral-600 dark:text-neutral-400">This page isn&apos;t available.</p>
      <Link href="/" className="mt-6 inline-block font-medium text-blue-600 hover:underline dark:text-blue-400">
        Back to home
      </Link>
    </div>
  );
}
