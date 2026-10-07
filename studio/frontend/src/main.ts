import { mount } from 'svelte';
import App from './App.svelte';
import { applyAppearance, loadAppearance } from './appearance';
import './app.css';
import './files/files.css';

const { theme, textScale } = loadAppearance();
applyAppearance(theme, textScale);
mount(App, { target: document.getElementById('app')! });
