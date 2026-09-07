"use client";

import type { ComponentProps } from "react";
import Link from "next/link";
import { useDirtyNavigation } from "@/components/dirty-guard";

export function GuardedLink({ onClick, ...props }: ComponentProps<typeof Link>) {
  const { confirmNavigation } = useDirtyNavigation();
  return (
    <Link
      {...props}
      onClick={(event) => {
        if (!confirmNavigation()) {
          event.preventDefault();
          return;
        }
        onClick?.(event);
      }}
    />
  );
}
