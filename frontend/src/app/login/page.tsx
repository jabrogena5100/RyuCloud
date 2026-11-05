"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { AuthForm } from "@/components/AuthForm";
import { useAuth } from "@/lib/auth-context";

export default function LoginPage() {
  const { login } = useAuth();
  const router = useRouter();

  return (
    <div className="mx-auto mt-6 w-full max-w-sm rounded-2xl border border-neutral-200 p-6 dark:border-neutral-800">
      <h1 className="mb-1 text-2xl font-semibold">Sign in</h1>
      <p className="mb-6 text-sm text-neutral-600 dark:text-neutral-400">to continue to RyuCloud</p>
      <AuthForm
        mode="login"
        onSubmit={async ({ email, password }) => {
          await login(email, password);
          router.push("/");
        }}
      />
      <p className="mt-6 text-sm text-neutral-600 dark:text-neutral-400">
        New here?{" "}
        <Link href="/register" className="font-medium text-blue-600 hover:underline dark:text-blue-400">
          Create an account
        </Link>
      </p>
    </div>
  );
}
