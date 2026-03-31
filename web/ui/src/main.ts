import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'

// Carbon pre-built CSS (SCSS theming deferred to avoid webpack dependency issues)
import 'carbon-components/css/carbon-components.min.css'
import './styles/global.css'

// Import Carbon Vue components individually (the default plugin uses
// require.context which is webpack-specific and incompatible with Vite)
import {
  CvHeader,
  CvHeaderName,
  CvHeaderNav,
  CvHeaderMenuItem,
  CvHeaderMenuButton,
  CvContent,
  CvSkipToContent,
  CvLoading,
  CvButton,
  CvTag,
  CvTabs,
  CvTab,
  CvTextInput,
  CvSearch,
  CvProgress,
  CvProgressStep,
  CvRadioButton,
  CvRadioGroup,
  CvInlineNotification,
  CvModal,
  CvInlineLoading,
  CvToggle,
  CvTile,
} from '@carbon/vue'

const app = createApp(App)
app.use(createPinia())
app.use(router)

// Register Carbon components globally
app.component('CvHeader', CvHeader)
app.component('CvHeaderName', CvHeaderName)
app.component('CvHeaderNav', CvHeaderNav)
app.component('CvHeaderMenuItem', CvHeaderMenuItem)
app.component('CvHeaderMenuButton', CvHeaderMenuButton)
app.component('CvContent', CvContent)
app.component('CvSkipToContent', CvSkipToContent)
app.component('CvLoading', CvLoading)
app.component('CvButton', CvButton)
app.component('CvTag', CvTag)
app.component('CvTabs', CvTabs)
app.component('CvTab', CvTab)
app.component('CvTextInput', CvTextInput)
app.component('CvSearch', CvSearch)
app.component('CvProgress', CvProgress)
app.component('CvProgressStep', CvProgressStep)
app.component('CvRadioButton', CvRadioButton)
app.component('CvRadioGroup', CvRadioGroup)
app.component('CvInlineNotification', CvInlineNotification)
app.component('CvModal', CvModal)
app.component('CvInlineLoading', CvInlineLoading)
app.component('CvToggle', CvToggle)
app.component('CvTile', CvTile)

app.mount('#app')
