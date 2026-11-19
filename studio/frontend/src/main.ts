import { mount } from 'svelte';
import App from './App.svelte';
import { applyAppearance, loadAppearance } from './appearance';
import { initializeUIStorage } from './uiStorage';
import './theme.css';
import './app.css';
import './files/files.css';

async function start() {
  await initializeUIStorage();
  applyAppearance(loadAppearance());
  mount(App, { target: document.getElementById('app')! });
}

void start();
