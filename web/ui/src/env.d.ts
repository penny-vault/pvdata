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
  export const CvTag: any
  export const CvTabs: any
  export const CvTab: any
  export const CvTextInput: any
  export const CvSearch: any
  export const CvProgress: any
  export const CvProgressStep: any
  export const CvRadioButton: any
  export const CvRadioGroup: any
  export const CvInlineNotification: any
  export const CvModal: any
  export const CvInlineLoading: any
  export const CvToggle: any
  export const CvTile: any
  export const CvDataTable: any
  export const CvDataTableAction: any
  export const CvDataTableCell: any
  export const CvDataTableHeading: any
  export const CvDataTableRow: any
  export const CvPagination: any
  export const CvSelect: any
}

declare module '@codemirror/state' {
  export class EditorState {
    static create(config?: any): EditorState
    doc: any
    update(spec: any): any
  }
}
