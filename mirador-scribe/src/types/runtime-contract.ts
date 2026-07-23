import ScribeAnnotationAdapterImplementation from '../annotationAdapter/ScribeAnnotationAdapter';
import type { ScribeAnnotationAdapterConstructor } from './scribe';

// This assignment is the compile-time link between the checked JS
// implementation and the declaration shipped to package consumers. Any
// signature drift fails `npm run check:types` before a package can be built.
const adapterConstructorContract: ScribeAnnotationAdapterConstructor = ScribeAnnotationAdapterImplementation;

void adapterConstructorContract;
