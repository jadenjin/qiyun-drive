import { PasswordReset } from "../../token-flows";

export default async function PasswordResetPage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;
  return <PasswordReset token={token} />;
}
