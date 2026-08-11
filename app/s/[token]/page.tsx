import { PublicShare } from "../../token-flows";

export default async function PublicSharePage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;
  return <PublicShare token={token} />;
}
