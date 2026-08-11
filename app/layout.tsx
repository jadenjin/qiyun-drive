import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  metadataBase: new URL(process.env.PUBLIC_BASE_URL || "http://localhost:3000"),
  title: "栖云 · 家庭私人云盘",
  description: "安全存放文件、照片与家人的共同回忆。",
  openGraph: {
    title: "栖云 · 家庭私人云盘",
    description: "把重要的东西，留在真正属于你的地方。",
    images: [{ url: "/og.png", width: 1536, height: 1024, alt: "栖云家庭私人云盘" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "栖云 · 家庭私人云盘",
    description: "把重要的东西，留在真正属于你的地方。",
    images: ["/og.png"],
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN">
      <body
        className={`${geistSans.variable} ${geistMono.variable} antialiased`}
      >
        {children}
      </body>
    </html>
  );
}
