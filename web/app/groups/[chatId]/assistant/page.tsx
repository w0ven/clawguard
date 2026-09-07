"use client";

import { useParams } from "next/navigation";
import { GroupAssistantWorkspace } from "@/components/group-assistant-workspace";

export default function GroupAssistantPage() {
  const params = useParams<{ chatId: string }>();
  return <GroupAssistantWorkspace chatId={Number(params.chatId)} />;
}
