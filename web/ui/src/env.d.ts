/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}

declare module '@carbon/vue' {
  import type { Plugin } from 'vue'
  const plugin: Plugin
  export default plugin
  export const CvHeader: any
  export const CvHeaderName: any
  export const CvHeaderNav: any
  export const CvHeaderMenuItem: any
  export const CvHeaderMenuButton: any
  export const CvContent: any
  export const CvSkipToContent: any
  export const CvSideNav: any
  export const CvSideNavItems: any
  export const CvSideNavLink: any
  export const CvLoading: any
  export const CvButton: any
}
