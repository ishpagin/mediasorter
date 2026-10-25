import { createRouter, createWebHistory } from 'vue-router'
import FilesPage from './pages/FilesPage.vue'
import PeoplePage from './pages/PeoplePage.vue'
import PersonPage from './pages/PersonPage.vue'
import OnlyPage from './pages/OnlyPage.vue'
import SimilarPage from './pages/SimilarPage.vue'
import SearchPage from './pages/SearchPage.vue'
import StacksPage from './pages/StacksPage.vue'
import CopiesPage from './pages/CopiesPage.vue'
import CollectionsPage from './pages/CollectionsPage.vue'

// same addresses as the old UI, so bookmarks keep working; filters live in the query string
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/people' },
    { path: '/files', component: FilesPage, props: { view: 'files', title: 'Все фото и видео' } },
    { path: '/no-people', component: FilesPage, props: { view: 'nopeople', title: 'Без людей' } },
    { path: '/strangers', component: FilesPage, props: { view: 'strangers', title: 'Только посторонние' } },
    { path: '/videos', component: FilesPage, props: { view: 'videos', title: 'Видео' } },
    { path: '/people', component: PeoplePage },
    { path: '/person', component: PersonPage },
    { path: '/only', component: OnlyPage },
    { path: '/similar', component: SimilarPage },
    { path: '/search', component: SearchPage },
    { path: '/stacks', component: StacksPage },
    { path: '/copies', component: CopiesPage },
    { path: '/collections', component: CollectionsPage },
    { path: '/:rest(.*)*', redirect: '/files' },
  ],
})
