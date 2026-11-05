"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { AuthForm } from "@/components/AuthForm";
import { useAuth } from "@/lib/auth-context";

export default function RegisterPage() {
  const { register } = useAuth();
  const router = useRouter();

  return (
    <div className="mx-auto mt-6 w-full max-w-sm rounded-2xl border border-neutral-200 p-6 dark:border-neutral-800">
      <h1 className="mb-1 text-2xl font-semibold">Create your account</h1>
      <p className="mb-6 text-sm text-neutral-600 dark:text-neutral-400">Join RyuCloud</p>
      <AuthForm
        mode="register"
        onSubmit={async (values) => {
          await register(values);
          router.push("/");
        }}
      />
      <p className="mt-6 text-sm text-neutral-600 dark:text-neutral-400">
        Already have an account?{" "}
        <Link href="/login" className="font-medium text-blue-600 hover:underline dark:text-blue-400">
          Sign in
        </Link>
      </p>
    </div>
  );
}
