# mediasorter-web frontend

Vue 3 + Vue Router + Pinia + VueUse, UI components from **shadcn-vue** (copied into `src/components/ui`, our own
code; built on Reka UI) with Tailwind CSS 4 and Lucide icons. All MIT/ISC/Apache, no license keys.
Built into `dist/` and embedded into the Go binary (`../build.sh`). Works offline: the Onest font is local.

    npm run dev                          # dev server; /api, /preview, /original go to MS_API (default: the server copy)
    npm run build                        # type check + production build into dist/
    npx shadcn-vue@latest add <name>     # add a component into src/components/ui
