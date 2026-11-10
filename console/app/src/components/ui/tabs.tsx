import * as React from "react"
import { cn } from "cn"
import { Tabs as TabsPrimitive } from "radix-ui"

// Weftbase segmented tabs (Tabs README): a tab-list ground with a 1px
// border, 2px padding; the list is 28px high, each tab 22px, 13px
// medium; the selected tab takes the page background and elev-tab-active.
// The classes are exported so link tabs (a view held in the URL) look
// the same as Radix tabs.
const tabsListClass =
  "inline-flex h-auto w-fit items-center h-7 rounded-input border border-tab-list-border bg-tab-list p-0.5 text-tab-foreground"

const tabTriggerClass =
  "inline-flex h-[22px] flex-none items-center justify-center gap-1.5 rounded-tab px-2 text-sm font-medium whitespace-nowrap text-tab-foreground transition-colors outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-tab-active data-[state=active]:text-tab-active-foreground data-[state=active]:shadow-tab-active aria-[current=page]:bg-tab-active aria-[current=page]:text-tab-active-foreground aria-[current=page]:shadow-tab-active [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5"

function Tabs({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  return (
    <TabsPrimitive.Root
      data-slot="tabs"
      className={cn("flex flex-col gap-3", className)}
      {...props}
    />
  )
}

function TabsList({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn(tabsListClass, className)}
      {...props}
    />
  )
}

function TabsTrigger({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(tabTriggerClass, className)}
      {...props}
    />
  )
}

function TabsContent({
  className,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn("flex-1 text-sm outline-none", className)}
      {...props}
    />
  )
}

export {
  Tabs,
  TabsList,
  TabsTrigger,
  TabsContent,
  tabsListClass,
  tabTriggerClass,
}
