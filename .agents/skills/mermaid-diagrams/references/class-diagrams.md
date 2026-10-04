# Class Diagrams

Class diagrams model object-oriented designs and domain models. They show entities (classes), their attributes/methods, and relationships.

## Basic Syntax

```mermaid
classDiagram
    ClassName
```

## Defining Classes with Members

```mermaid
classDiagram
    class BankAccount {
        +String owner
        +Decimal balance
        -String accountNumber
        +deposit(amount)
        +withdraw(amount)
        +getBalance() Decimal
    }
```

**Visibility modifiers:**
- `+` Public
- `-` Private
- `#` Protected
- `~` Package/Internal

**Member syntax:**
- `+type attribute` - Attribute with type
- `+method(params) ReturnType` - Method with parameters and return type

## Relationships

### Association (`--`)
Loose relationship where entities use each other but exist independently.

```mermaid
classDiagram
    Title -- Genre
```

### Composition (`*--`)
Strong ownership - child cannot exist without parent. When parent is deleted, children are deleted.

```mermaid
classDiagram
    Order *-- LineItem
    House *-- Room
```

### Aggregation (`o--`)
Weaker ownership - child can exist independently. Represents "has-a" relationship.

```mermaid
classDiagram
    Department o-- Employee
    Playlist o-- Song
```

### Inheritance (`<|--`)
"Is-a" relationship. Child class inherits from parent class.

```mermaid
classDiagram
    Animal <|-- Dog
    Animal <|-- Cat
    
    class Animal {
        +String name
        +makeSound()
    }
    
    class Dog {
        +bark()
    }
```

### Dependency (`<..`)
One class depends on another, often as a parameter or local variable.

```mermaid
classDiagram
    OrderProcessor <.. PaymentGateway
```

### Realization/Implementation (`<|..`)
Class implements an interface.

```mermaid
classDiagram
    class Drawable {
        <<interface>>
        +draw()
    }
    Drawable <|.. Circle
    Drawable <|.. Rectangle
```

## Multiplicity

Show how many instances participate in a relationship:

```mermaid
classDiagram
    Customer "1" --> "0..*" Order : places
    Order "1" *-- "1..*" LineItem : contains
    Author "1..*" -- "1..*" Book : writes
```

**Common multiplicities:**
- `1` - Exactly one
- `0..1` - Zero or one
- `0..*` or `*` - Zero or many
- `1..*` - One or many
- `m..n` - Between m and n

## Relationship Labels

```mermaid
classDiagram
    Customer --> Order : places
    Order --> Product : contains
    Driver --> Vehicle : drives
```

## Class Stereotypes

Mark special class types:

```mermaid
classDiagram
    class IRepository {
        <<interface>>
        +save(entity)
        +findById(id)
    }
    
    class UserService {
        <<service>>
        +createUser()
    }
    
    class UserDTO {
        <<dataclass>>
        +String name
        +String email
    }
```

## Abstract Classes and Methods

```mermaid
classDiagram
    class Shape {
        <<abstract>>
        +int x
        +int y
        +draw()* abstract
        +move(x, y)
    }
    
    Shape <|-- Circle
    Shape <|-- Rectangle
```

## Generic Classes

```mermaid
classDiagram
    class List~T~ {
        +add(item: T)
        +get(index: int) T
    }
    
    List~String~ <-- StringProcessor
```

## Comprehensive Example: E-Commerce Domain

```mermaid
classDiagram
    %% Core entities
    class Customer {
        +UUID id
        +String email
        +String name
        +Address shippingAddress
        +placeOrder(cart: Cart) Order
        +getOrderHistory() List~Order~
    }
    
    class Order {
        +UUID id
        +DateTime orderDate
        +OrderStatus status
        +Decimal total
        +calculateTotal() Decimal
        +ship()
        +cancel()
    }
    
    class LineItem {
        +int quantity
        +Decimal pricePerUnit
        +getSubtotal() Decimal
    }
    
    class Product {
        +UUID id
        +String name
        +String description
        +Decimal price
        +int stockQuantity
        +reduceStock(quantity: int)
        +isAvailable() bool
    }
    
    class Category {
        +String name
        +String description
    }
    
    class Cart {
        +addItem(product: Product, quantity: int)
        +removeItem(product: Product)
        +getTotal() Decimal
        +clear()
    }
    
    %% Relationships
    Customer "1" --> "0..*" Order : places
    Customer "1" --> "1" Cart : has
    Order "1" *-- "1..*" LineItem : contains
    LineItem "1" --> "1" Product : references
    Product "0..*" --> "1" Category : belongs to
    Cart "1" o-- "0..*" Product : contains
    
    %% Enums
    class OrderStatus {
        <<enumeration>>
        PENDING
        PAID
        SHIPPED
        DELIVERED
        CANCELLED
    }
    
    Order --> OrderStatus
```

## Domain-Driven Design Patterns

### Entities
```mermaid
classDiagram
    class User {
        <<entity>>
        -UUID id
        +String email
        +String name
    }
```

### Value Objects
```mermaid
classDiagram
    class Money {
        <<value object>>
        +Decimal amount
        +String currency
        +add(other: Money) Money
    }
    
    class Address {
        <<value object>>
        +String street
        +String city
        +String postalCode
    }
```

### Aggregates
```mermaid
classDiagram
    class Order {
        <<aggregate root>>
        -UUID id
        +addLineItem(item)
        +removeLineItem(item)
    }
    
    Order *-- LineItem
```

## Tips for Effective Class Diagrams

1. **Start with core entities** - Add attributes and methods incrementally
2. **Show only relevant details** - Omit obvious getters/setters unless important
3. **Use appropriate relationships** - Choose between association, aggregation, and composition carefully
4. **Add multiplicity** - Clarifies how many instances participate
5. **Group related classes** - Use notes or visual proximity
6. **Document invariants** - Use notes to explain business rules

## Common Patterns

### Repository Pattern
```mermaid
classDiagram
    class IRepository~T~ {
        <<interface>>
        +save(entity: T)
        +findById(id: UUID) T
        +delete(entity: T)
    }
    
    class UserRepository {
        +findByEmail(email: String) User
    }
    
    IRepository~User~ <|.. UserRepository
```

### Factory Pattern
```mermaid
classDiagram
    class ShapeFactory {
        +createShape(type: String) Shape
    }
    
    class Shape {
        <<abstract>>
        +draw()*
    }
    
    ShapeFactory ..> Shape : creates
    Shape <|-- Circle
    Shape <|-- Rectangle
```

### Strategy Pattern
```mermaid
classDiagram
    class PaymentProcessor {
        -PaymentStrategy strategy
        +setStrategy(strategy: PaymentStrategy)
        +processPayment(amount: Decimal)
    }
    
    class PaymentStrategy {
        <<interface>>
        +pay(amount: Decimal)*
    }
    
    PaymentStrategy <|.. CreditCardPayment
    PaymentStrategy <|.. PayPalPayment
    PaymentProcessor --> PaymentStrategy
```
