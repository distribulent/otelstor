import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'

const target = document.getElementById('app')
const app = target ? mount(App, { target }) : undefined

export default app
