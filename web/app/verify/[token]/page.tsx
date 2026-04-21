import { TurnstileVerifyClient } from "@/components/turnstile-verify-client";

type VerifyPageProps = {
  params: Promise<{ token: string }>;
};

export default async function VerifyPage({ params }: VerifyPageProps) {
  const { token } = await params;

  return <TurnstileVerifyClient token={token} />;
}
