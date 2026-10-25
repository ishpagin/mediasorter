import { createApp } from 'vue'
import { createPinia } from 'pinia'
import './style.css'
import App from './App.vue'
import { router } from './router'

// dark by default: photos read best on a dark surround; the choice is remembered
if (localStorage.getItem('ms-theme') !== 'light') document.documentElement.classList.add('dark')

createApp(App).use(createPinia()).use(router).mount('#app')
