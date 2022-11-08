import { useState, useRef } from 'react'
import { Combobox } from '@headlessui/react'
import { PlusIcon } from '@heroicons/react/outline'


export default function AllowedDomainsForm({onSubmit = () => {}}) {
  const [domain, setDomain] = useState('')

  const button = useRef()

  return (
    <div className='my-2 flex flex-row space-x-3'>
      <div className='flex flex-1 items-center'>
        <Combobox
          as='div'
          className='relative flex-1'
          value={domain}
          onChange={setDomain}
        >
          <Combobox.Input
            className={`block w-full rounded-md border-gray-300 text-xs shadow-sm focus:border-blue-500 focus:ring-blue-500`}
            placeholder='Enter a domain'
            onChange={e => {
              setDomain(e.target.value)
            }}
            onFocus={() => {
              if (domain === '') {
                button.current?.click()
              }
            }}
          />
          <Combobox.Button className='hidden' ref={button} />
        </Combobox>
      </div>
      <div className='relative'>
        <button
          disabled={domain === ''}
          onClick={() => {
            onSubmit(domain)
            setDomain('')
          }}
          form='allowed-domains'
          className='inline-flex items-center rounded-md border border-transparent bg-black px-4 py-[7px] text-xs font-medium text-white shadow-sm hover:cursor-pointer hover:bg-gray-800 disabled:cursor-not-allowed disabled:opacity-30'
        >
          <PlusIcon className='mr-1 h-3 w-3' />
          Add
        </button>
      </div>
    </div>
  )
}
